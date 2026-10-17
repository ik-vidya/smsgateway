// smsgateway admin UI — клиентские хелперы.
//
// /logs — SSE-поток логов.
// /setup — SSE-поток портов, probe, save и самопроверка.
(function () {
    'use strict';

    initLogs();
    initSetup();
    initSetupTest();

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
            });
        }

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
        };

        var probedPort = '';

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
                li.innerHTML =
                    '<code class="flex-grow-1">' + escapeHtml(p) + '</code>' +
                    '<button type="button" class="btn btn-sm btn-primary setup-probe"' +
                    ' data-port="' + escapeHtml(p) + '">' +
                    escapeHtml(labels.probe) + '</button>';
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
                // Через 5 секунд перезагрузить /setup — exec уже
                // произошёл, страница отрендерится с новым
                // CurrentPort и кнопкой «Тест».
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

    // ==================== /setup/test ====================

    function initSetupTest() {
        var card = document.getElementById('setup-test-card');
        if (!card) return;

        var btn = document.getElementById('setup-test-btn');
        var result = document.getElementById('setup-test-result');
        if (!btn || !result) return;

        var labels = {
            test: card.dataset.testLabel,
            testing: card.dataset.testingLabel,
            selfNumber: card.dataset.selfNumber,
            ok: card.dataset.testOk,
            timeout: card.dataset.testTimeout,
            failed: card.dataset.testFailed,
            noSelf: card.dataset.testNoSelf,
        };

        btn.addEventListener('click', function () {
            btn.disabled = true;
            btn.textContent = labels.testing;
            result.classList.remove('d-none');
            result.className = 'mt-3 alert alert-secondary';
            result.textContent = '…';

            fetch('/setup/test', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: '{}',
            }).then(function (resp) {
                return resp.json().then(function (j) {
                    return { status: resp.status, body: j };
                });
            }).then(function (r) {
                showTestResult(r.status, r.body);
            }).catch(function (err) {
                showTestResult(0, { error: String(err) });
            }).finally(function () {
                btn.disabled = false;
                btn.textContent = labels.test;
            });
        });

        function showTestResult(status, body) {
            result.classList.remove('d-none');
            if (status === 400 && body && body.error &&
                body.error.indexOf('SELF_NUMBER') >= 0) {
                result.className = 'mt-3 alert alert-warning';
                result.textContent = labels.noSelf;
                return;
            }
            if (status !== 200 || (body && body.error && !body.id)) {
                result.className = 'mt-3 alert alert-danger';
                var msg = (body && body.error) || 'error';
                result.textContent = labels.failed + ': ' + msg;
                return;
            }

            var statusName = body.status || '?';
            if (statusName === 'delivered') {
                result.className = 'mt-3 alert alert-success';
                result.textContent = labels.ok;
                return;
            }
            if (body.timed_out) {
                result.className = 'mt-3 alert alert-warning';
                result.textContent = labels.timeout;
                return;
            }
            if (statusName === 'failed') {
                result.className = 'mt-3 alert alert-danger';
                var errMsg = body.error || statusName;
                result.textContent = labels.failed + ': ' + errMsg;
                return;
            }
            // sent / sent_no_dr / received / queued — промежуточные.
            result.className = 'mt-3 alert alert-info';
            result.textContent = labels.ok + ' (status: ' + statusName + ')';
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
})();
