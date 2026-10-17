// smsgateway admin UI — клиентские хелперы.
//
// На странице /logs: подключается к /logs/stream через EventSource,
// рисует новые записи в #logs, ограничивает размер DOM, следует
// за хвостом (если пользователь не отматывал вверх).
(function () {
    'use strict';

    var container = document.getElementById('logs');
    if (!container) return;

    var MAX_LINES = 2000;
    var LEVELS = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };

    var paused = false;
    var minLevel = 'INFO';
    var dropped = 0;

    function escapeHtml(s) {
        return String(s)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

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

        if (shouldScroll) {
            container.scrollTop = container.scrollHeight;
        }
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

    // ---- EventSource ----

    var es = new EventSource('/logs/stream');

    es.onopen = function () {
        // при переподключении — просто продолжаем
    };

    es.onmessage = function (ev) {
        try {
            var e = JSON.parse(ev.data);
            append(e);
        } catch (err) {
            console.error('logs: parse error', err);
        }
    };

    es.addEventListener('warning', function (ev) {
        console.warn('logs:', ev.data);
    });

    es.onerror = function () {
        // EventSource переподключается автоматически;
        // при окончательной ошибке (readyState=CLOSED) —
        // покажем индикатор.
        if (es.readyState === EventSource.CLOSED) {
            ensureNotEmpty();
            var line = document.createElement('div');
            line.className = 'log-line log-ERROR';
            line.textContent = '[stream closed]';
            container.appendChild(line);
        }
    };

    // ---- Управление ----

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
            // Перерисовать не нужно: старые записи остаются как есть,
            // новые фильтруются по новому уровню. Для «почистить
            // сейчас» есть Clear.
        });
    }

    showEmptyIfNeeded();
})();
