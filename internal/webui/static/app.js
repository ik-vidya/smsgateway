// smsgateway admin UI — клиентские хелперы.
//
// /logs — SSE-поток логов.
// /setup — SSE-поток портов, probe, save, диагностика и отправка+приём.
(function () {
    'use strict';

    initLogs();
    initSetup();

    // ==================== /logs ====================

    function initLogs() {
        var container = document.getElementById('logs');
        if (!container) return;

        var MAX_LINES = 2000;
        var LEVELS = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };
        var paused = false;
        var minLevel = 'INFO';

        function isAtBottom() {
            return container.scrollTop + container.clientHeight
                >= container.scrollHeight - 40;
        }

        function ensureNotEmpty() {
            var empty = container.querySelector('.logs-empty');
            if (empty) empty.remove();
        }

        function showEmptyIfNeeded() {
            if (container.children.length > 0) return;
            var empty = document.createElement('div');
            empty.className = 'logs-empty text-muted small p-2';
            empty.textContent = container.dataset.empty || 'No log entries yet.';
            container.appendChild(empty);
        }

        function append(e) {
            if (LEVELS[e.level] === undefined) return;
            if (LEVELS[e.level] < LEVELS[minLevel]) return;

            ensureNotEmpty();
            var shouldScroll = isAtBottom() && !paused;

            var line = document.createElement('div');
            line.className = 'log-line log-' + e.level;

            var attrs = '';
            if (e.attrs) {
                attrs = Object.keys(e.attrs)
                    .sort()
                    .map(function (k) { return k + '=' + e.attrs[k]; })
                    .join(' ');
            }

            line.innerHTML =
                '<span class="log-time">' + escapeHtml(formatTime(e.time)) + '</span> ' +
                '<span class="log-level">' + escapeHtml(e.level) + '</span> ' +
                '<span class="log-msg">' + escapeHtml(e.msg) + '</span>' +
                (attrs ? ' <span class="log-attrs">' + escapeHtml(attrs) + '</span>' : '');

            container.appendChild(line);

            while (container.children.length > MAX_LINES) {
                container.removeChild(container.firstChild);
            }
            if (shouldScroll) container.scrollTop = container.scrollHeight;
        }

        var es = new EventSource('/logs/stream');
        es.onmessage = function (ev) {
            try { append(JSON.parse(ev.data)); }
            catch (err) { console.error('logs: parse', err); }
        };
        es.addEventListener('warning', function (ev) {
            console.warn('logs:', ev.data);
        });
        es.onerror = function () {
            if (es.readyState === EventSource.CLOSED) {
                ensureNotEmpty();
                var line = document.createElement('div');
                line.className = 'log-line log-ERROR';
                line.textContent = '[stream closed]';
                container.appendChild(line);
            }
        };

        var pauseBtn = document.getElementById('logs-pause');
        if (pauseBtn) {
            pauseBtn.addEventListener('click', function () {
                paused = !paused;
                pauseBtn.textContent = paused
                    ? pauseBtn.dataset.resumeLabel
                    : pauseBtn.dataset.pauseLabel;
                pauseBtn.classList.toggle('btn-warning', paused);
                pauseBtn.classList.toggle('btn-outline-secondary', !paused);
            });
        }

        var clearBtn = document.getElementById('logs-clear');
        if (clearBtn) {
            clearBtn.addEventListener('click', function () {
                container.innerHTML = '';
                showEmptyIfNeeded();
            });
        }

        var levelSelect = document.getElementById('logs-level');
        if (levelSelect) {
            levelSelect.addEventListener('change', function () {
                minLevel = levelSelect.value;
                updateDownloadLinks();
            });
        }

        var searchInput = document.getElementById('logs-search');
        var searchNeedle = '';
        if (searchInput) {
            searchInput.addEventListener('input', function () {
                searchNeedle = searchInput.value.trim().toLowerCase();
                applySearch();
                updateDownloadLinks();
            });
        }

        function lineMatchesSearch(text) {
            if (searchNeedle === '') return true;
            return text.toLowerCase().indexOf(searchNeedle) !== -1;
        }

        function applySearch() {
            var lines = container.querySelectorAll('.log-line');
            for (var i = 0; i < lines.length; i++) {
                var el = lines[i];
                var text = el.dataset.rawText || el.textContent || '';
                el.style.display = lineMatchesSearch(text) ? '' : 'none';
            }
        }

        function updateDownloadLinks() {
            var level = (levelSelect && levelSelect.value) || 'DEBUG';
            var levelQ = level === 'DEBUG' ? '' : level.toLowerCase();
            var containsQ = (searchInput && searchInput.value.trim()) || '';
            var qs = [];
            if (levelQ) qs.push('level=' + encodeURIComponent(levelQ));
            if (containsQ) qs.push('contains=' + encodeURIComponent(containsQ));
            var base = '/logs/download';
            if (qs.length) base += '?' + qs.join('&');
            var a1 = document.getElementById('logs-download');
            var a2 = document.getElementById('logs-download-json');
            if (a1) a1.href = base + (qs.length ? '&' : '?') + 'format=text';
            if (a2) a2.href = base + (qs.length ? '&' : '?') + 'format=json';
        }

        // rawText для каждой строки, чтобы поиск работал.
        var appendOrig = append;
        append = function(e) {
            appendOrig(e);
            var last = container.lastElementChild;
            if (last && last.classList.contains('log-line') && !last.dataset.rawText) {
                last.dataset.rawText = e.level + ' ' + e.msg;
                if (!lineMatchesSearch(last.dataset.rawText)) {
                    last.style.display = 'none';
                }
            }
        };

        updateDownloadLinks();
        showEmptyIfNeeded();
    }

    // ==================== /setup ====================

    function initSetup() {
        var list = document.getElementById('setup-ports-list');
        if (!list) return;

        var items = document.getElementById('setup-ports-items');
        var empty = document.getElementById('setup-ports-empty');
        var result = document.getElementById('setup-result');

        var labels = {
            probe: list.dataset.probe,
            probing: list.dataset.probing,
            manufacturer: list.dataset.manufacturer,
            model: list.dataset.model,
            revision: list.dataset.revision,
            imei: list.dataset.imei,
            save: list.dataset.save,
            saving: list.dataset.saving,
            savingHint: list.dataset.savingHint,
            saved: list.dataset.saved,
            saveFailed: list.dataset.saveFailed,
            portBusy: list.dataset.portBusy,
            portInUse: list.dataset.portInUse,
        };

        var probedPort = '';
        var activePort = list.dataset.activePort || '';

        function renderPorts(ports) {
            items.innerHTML = '';
            if (!ports || ports.length === 0) {
                empty.classList.remove('d-none');
                list.classList.add('d-none');
                return;
            }
            empty.classList.add('d-none');
            list.classList.remove('d-none');
            ports.forEach(function (p) {
                var li = document.createElement('li');
                li.className = 'setup-port-item d-flex align-items-center gap-2 mb-2';
                var isActive = (p === activePort);
                var action;
                if (isActive) {
                    action = '<span class="badge bg-secondary">' +
                        escapeHtml(labels.portInUse) + '</span>';
                } else {
                    action = '<button type="button" class="btn btn-sm btn-primary setup-probe"' +
                        ' data-port="' + escapeHtml(p) + '">' +
                        escapeHtml(labels.probe) + '</button>';
                }
                li.innerHTML =
                    '<code class="flex-grow-1">' + escapeHtml(p) + '</code>' +
                    action;
                items.appendChild(li);
            });
        }

        items.addEventListener('click', function (ev) {
            var btn = ev.target.closest('.setup-probe');
            if (!btn) return;
            var port = btn.dataset.port;
            btn.disabled = true;
            btn.textContent = labels.probing;

            fetch('/setup/probe', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ port: port }),
            }).then(function (resp) {
                return resp.json().then(function (j) {
                    return { status: resp.status, body: j };
                });
            }).then(function (r) {
                showResult(port, r.status, r.body);
            }).catch(function (err) {
                showResult(port, 0, { error: String(err) });
            }).finally(function () {
                btn.disabled = false;
                btn.textContent = labels.probe;
            });
        });

        function showResult(port, status, body) {
            result.classList.remove('d-none');
            if (status !== 200 || (body && body.error)) {
                result.className = 'alert alert-danger mt-3';
                var msg = (body && body.error) || 'error';
                if (body && body.error_code === 'port_busy' && labels.portBusy) {
                    msg = labels.portBusy;
                }
                result.innerHTML = '<strong>' + escapeHtml(port) + '</strong>: ' +
                    escapeHtml(msg);
                probedPort = '';
                return;
            }
            result.className = 'alert alert-success mt-3';
            var lines = [
                labels.manufacturer + ': ' + (body.manufacturer || '—'),
                labels.model + ': ' + (body.model || '—'),
                labels.revision + ': ' + (body.revision || '—'),
                labels.imei + ': ' + (body.imei || '—'),
            ];
            result.innerHTML = '<strong>' + escapeHtml(port) + '</strong><br>' +
                lines.map(escapeHtml).join('<br>') +
                '<div class="mt-3">' +
                '<button type="button" class="btn btn-success setup-save"' +
                ' data-port="' + escapeHtml(port) + '">' +
                escapeHtml(labels.save) + '</button>' +
                '</div>';
            probedPort = port;
        }

        result.addEventListener('click', function (ev) {
            var btn = ev.target.closest('.setup-save');
            if (!btn) return;
            var port = btn.dataset.port || probedPort;
            if (!port) return;
            btn.disabled = true;
            btn.textContent = labels.saving;

            fetch('/setup/save', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ port: port }),
            }).then(function (resp) {
                return resp.json().then(function (j) {
                    return { status: resp.status, body: j };
                });
            }).then(function (r) {
                if (r.status !== 200 || (r.body && r.body.error)) {
                    btn.disabled = false;
                    btn.textContent = labels.save;
                    var msg = (r.body && r.body.error) || 'error';
                    result.className = 'alert alert-danger mt-3';
                    result.innerHTML = '<strong>' + escapeHtml(port) + '</strong>: ' +
                        escapeHtml(labels.saveFailed) + ' — ' + escapeHtml(msg);
                    return;
                }
                result.className = 'alert alert-info mt-3';
                result.innerHTML = escapeHtml(labels.saved) + '<br>' +
                    '<small class="text-muted">' +
                    escapeHtml(labels.savingHint) + '</small>';
                setTimeout(function () {
                    window.location.href = '/setup';
                }, 5000);
            }).catch(function (err) {
                btn.disabled = false;
                btn.textContent = labels.save;
                result.className = 'alert alert-danger mt-3';
                result.innerHTML = escapeHtml(labels.saveFailed) +
                    ': ' + escapeHtml(String(err));
            });
        });

        var es = new EventSource('/setup/ports/stream');
        es.onmessage = function (ev) {
            try {
                var data = JSON.parse(ev.data);
                renderPorts(data.ports || []);
            } catch (err) {
                console.error('setup: parse', err);
            }
        };
        es.onerror = function () {
            if (es.readyState === EventSource.CLOSED) {
                console.warn('setup: stream closed');
            }
        };
    }

    // ==================== setup: diagnostics + test-receive ====================

    function initSetupDiagnostics() {
        var card = document.getElementById('setup-diag-card');
        if (!card) return;

        var btn = document.getElementById('setup-diag-btn');
        var result = document.getElementById('setup-diag-result');
        if (!btn || !result) return;

        var labels = {
            refresh: card.dataset.refresh,
            loading: card.dataset.loading,
            error: card.dataset.error,
            manufacturer: card.dataset.manufacturer,
            model: card.dataset.model,
            revision: card.dataset.revision,
            imei: card.dataset.imei,
            imsi: card.dataset.imsi,
            iccid: card.dataset.iccid,
            sim: card.dataset.sim,
            signal: card.dataset.signal,
            registration: card.dataset.registration,
            gprs: card.dataset.gprs,
            operator: card.dataset.operator,
            smsc: card.dataset.smsc,
            storage: card.dataset.storage,
        };

        btn.addEventListener('click', function () {
            btn.disabled = true;
            var orig = btn.textContent;
            btn.textContent = labels.loading;
            result.innerHTML = '<span class="text-muted">' +
                escapeHtml(labels.loading) + '</span>';

            fetch('/setup/diagnostics', { method: 'GET' })
                .then(function (resp) {
                    return resp.json().then(function (j) {
                        return { status: resp.status, body: j };
                    });
                })
                .then(function (r) { render(r.status, r.body); })
                .catch(function (err) {
                    result.innerHTML = '<div class="alert alert-danger">' +
                        escapeHtml(labels.error + ': ' + err) + '</div>';
                })
                .finally(function () {
                    btn.disabled = false;
                    btn.textContent = orig;
                });
        });

        function render(status, body) {
            if (status !== 200 || (body && body.error)) {
                var msg = (body && body.error) || 'error';
                result.innerHTML = '<div class="alert alert-danger">' +
                    escapeHtml(msg) + '</div>';
                return;
            }

            var rows = [];
            function row(label, value) {
                if (value === undefined || value === null || value === '') return;
                rows.push('<tr><th class="text-nowrap small">' +
                    escapeHtml(label) + '</th><td class="small">' +
                    escapeHtml(value) + '</td></tr>');
            }

            row(labels.manufacturer, body.manufacturer);
            row(labels.model, body.model);
            row(labels.revision, body.revision);
            row(labels.imei, body.imei);
            row(labels.imsi, body.imsi);
            row(labels.iccid, body.iccid);
            row(labels.sim, body.sim_status);

            if (body.signal) {
                var sig = body.signal;
                var sigText;
                if (sig.error) {
                    sigText = labels.error + ': ' + sig.error;
                    if (sig.cached_rssi) {
                        sigText += ' (cached: ' + sig.cached_rssi + '/31)';
                    }
                } else {
                    sigText = sig.rssi + '/31, ' + sig.dbm + ' dBm';
                    if (sig.quality) sigText += ' (' + sig.quality + ')';
                }
                row(labels.signal, sigText);
            }

            if (body.registration) {
                var reg = body.registration;
                var regText;
                if (reg.error) {
                    regText = labels.error + ': ' + reg.error;
                    if (reg.cached_text) {
                        regText += ' (cached: ' + reg.cached_text + ')';
                    }
                } else {
                    regText = reg.text || ('status ' + reg.status);
                    if (reg.lac) regText += ', LAC ' + reg.lac;
                    if (reg.cid) regText += ', CID ' + reg.cid;
                }
                row(labels.registration, regText);
            }

            if (body.gprs_registration) {
                var gprs = body.gprs_registration;
                var gprsText;
                if (gprs.error) {
                    gprsText = labels.error + ': ' + gprs.error;
                } else {
                    gprsText = gprs.text || ('status ' + gprs.status);
                }
                row(labels.gprs, gprsText);
            }

            if (body.operator) {
                var op = body.operator;
                var opText;
                if (op.error) opText = labels.error + ': ' + op.error;
                else if (op.name && op.code) opText = op.name + ' (' + op.code + ')';
                else opText = op.name || op.code || '';
                row(labels.operator, opText);
            }

            row(labels.smsc, body.smsc);

            if (body.storage) {
                var st = body.storage;
                var stText;
                if (st.error) {
                    stText = labels.error + ': ' + st.error;
                } else {
                    stText = st.read + ': ' + st.used + '/' + st.total;
                    if (st.write && st.write !== st.read) {
                        stText += ' (write: ' + st.write + ')';
                    }
                }
                row(labels.storage, stText);
            }

            if (rows.length === 0) {
                result.innerHTML = '<span class="text-muted">—</span>';
            } else {
                result.innerHTML = '<table class="table table-sm mb-0">' +
                    rows.join('') + '</table>';
            }
        }
    }

    function initSetupTestReceive() {
        var card = document.getElementById('setup-test-receive-card');
        if (!card) return;

        var btn = document.getElementById('setup-test-receive-btn');
        var result = document.getElementById('setup-test-receive-result');
        if (!btn || !result) return;

        var labels = {
            start: card.dataset.start,
            running: card.dataset.running,
            sent: card.dataset.sent,
            received: card.dataset.received,
            success: card.dataset.success,
            timeout: card.dataset.timeout,
            notApplicable: card.dataset.notApplicable,
            error: card.dataset.error,
        };

        btn.addEventListener('click', function () {
            btn.disabled = true;
            var orig = btn.textContent;
            btn.textContent = labels.running;
            result.classList.remove('d-none');
            result.className = 'mt-3 alert alert-secondary';
            result.innerHTML = '<span class="text-muted">' +
                escapeHtml(labels.running) + '</span>';

            fetch('/setup/test-receive', { method: 'POST' })
                .then(function (resp) {
                    return resp.json().then(function (j) {
                        return { status: resp.status, body: j };
                    });
                })
                .then(function (r) { render(r.status, r.body); })
                .catch(function (err) {
                    result.className = 'mt-3 alert alert-danger';
                    result.textContent = labels.error + ': ' + err;
                })
                .finally(function () {
                    btn.disabled = false;
                    btn.textContent = orig;
                });
        });

        function render(status, body) {
            if (status !== 200 || (body && body.error)) {
                result.className = 'mt-3 alert alert-danger';
                result.textContent = labels.error + ': ' +
                    ((body && body.error) || 'error');
                return;
            }

            var parts = [];

            var sent = body.sent || {};
            var sentText = labels.sent + ': ' + (sent.status || '?');
            if (sent.id) sentText += ' (id ' + sent.id + ')';
            if (sent.reference) sentText += ', ref ' + sent.reference;
            if (sent.timed_out) sentText += ' (timeout)';
            if (sent.error) sentText += ' — ' + sent.error;
            parts.push('<div class="mb-2">' + escapeHtml(sentText) + '</div>');

            var cls;
            if (body.received) {
                var r = body.received;
                var recvText = labels.received + ': id ' + r.id;
                if (r.text) recvText += ', «' + r.text + '»';
                parts.push('<div class="mb-2">' + escapeHtml(recvText) + '</div>');
                parts.push('<div class="fw-semibold text-success">&#10003; ' +
                    escapeHtml(labels.success) + '</div>');
                cls = 'alert-success';
            } else if (body.receive_timed_out) {
                parts.push('<div class="fw-semibold text-warning">' +
                    escapeHtml(labels.timeout) + '</div>');
                cls = 'alert-warning';
            } else if (body.receive_error) {
                parts.push('<div class="fw-semibold text-danger">' +
                    escapeHtml(labels.error + ': ' + body.receive_error) +
                    '</div>');
                cls = 'alert-danger';
            } else {
                parts.push('<div class="text-muted">' +
                    escapeHtml(labels.notApplicable) + '</div>');
                cls = 'alert-info';
            }

            result.className = 'mt-3 alert ' + cls;
            result.innerHTML = parts.join('');
        }
    }

    // ==================== общее ====================

    function escapeHtml(s) {
        return String(s)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    function formatTime(iso) {
        try {
            var d = new Date(iso);
            if (isNaN(d.getTime())) return iso;
            var hh = String(d.getHours()).padStart(2, '0');
            var mm = String(d.getMinutes()).padStart(2, '0');
            var ss = String(d.getSeconds()).padStart(2, '0');
            return hh + ':' + mm + ':' + ss;
        } catch (_) {
            return iso;
        }
    }

    // Вызовы новых функций.
    initSetupDiagnostics();
    initSetupTestReceive();
})();