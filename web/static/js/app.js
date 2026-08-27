// HomeMonitor client-side JS

document.addEventListener('DOMContentLoaded', function() {
    console.log('HomeMonitor UI loaded');
});

// Chart defaults
if (typeof Chart !== 'undefined') {
    Chart.defaults.color = '#9ca3af';
    Chart.defaults.borderColor = '#1f2937';
    Chart.defaults.font.size = 11;
}

function chartOpts(yLabel, isPercent) {
    return {
        responsive: true,
        maintainAspectRatio: false,
        animation: { duration: 300 },
        interaction: { mode: 'index', intersect: false },
        plugins: {
            legend: { display: true, position: 'top', labels: { boxWidth: 8, padding: 8, font: { size: 10 } } },
            tooltip: {
                backgroundColor: '#111827', borderColor: '#374151', borderWidth: 1,
                titleFont: { size: 11 }, bodyFont: { size: 11 },
                callbacks: isPercent ? { label: ctx => ctx.dataset.label + ': ' + ctx.parsed.y.toFixed(1) + '%' } : {}
            }
        },
        scales: {
            x: {
                type: 'category',
                ticks: { maxTicksLimit: 8, font: { size: 9 }, maxRotation: 0 },
                grid: { display: false }
            },
            y: {
                min: 0,
                max: isPercent ? 100 : undefined,
                ticks: {
                    font: { size: 9 },
                    callback: isPercent ? v => v + '%' : undefined
                },
                grid: { color: '#1f293780' }
            }
        }
    };
}

function formatNetRate(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
    if (bytes < 1073741824) return (bytes / 1048576).toFixed(1) + ' MB';
    return (bytes / 1073741824).toFixed(1) + ' GB';
}

function hostCharts(hostId) {
    return {
        hours: 24,
        charts: {},
        async load() {
            const res = await fetch('/api/v1/hosts/' + hostId + '/history?hours=' + this.hours);
            const data = await res.json();
            if (!data || !data.length) return;

            const labels = data.map(p => {
                const d = new Date(p.t);
                if (this.hours <= 6) return d.toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'});
                if (this.hours <= 24) return d.toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'});
                return d.toLocaleDateString([], {month:'short', day:'numeric'}) + ' ' + d.toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'});
            });

            // CPU chart
            this.renderChart('cpuChart', {
                labels,
                datasets: [{
                    label: 'CPU %', data: data.map(p => p.cpu),
                    borderColor: '#22c55e', backgroundColor: '#22c55e20',
                    borderWidth: 1.5, fill: true, pointRadius: 0, tension: 0.3
                }]
            }, chartOpts('CPU %', true));

            // Memory chart
            this.renderChart('memChart', {
                labels,
                datasets: [{
                    label: 'Memory %', data: data.map(p => p.mem),
                    borderColor: '#3b82f6', backgroundColor: '#3b82f620',
                    borderWidth: 1.5, fill: true, pointRadius: 0, tension: 0.3
                }]
            }, chartOpts('Memory %', true));

            // Load chart
            this.renderChart('loadChart', {
                labels,
                datasets: [
                    { label: 'Load 1m', data: data.map(p => p.load1), borderColor: '#f59e0b', borderWidth: 1.5, pointRadius: 0, tension: 0.3 },
                    { label: 'Load 5m', data: data.map(p => p.load5), borderColor: '#f59e0b80', borderWidth: 1, pointRadius: 0, tension: 0.3, borderDash: [4,2] }
                ]
            }, chartOpts('Load', false));

            // Network chart - show deltas between points
            const netSent = [], netRecv = [];
            for (let i = 0; i < data.length; i++) {
                if (i === 0) { netSent.push(0); netRecv.push(0); continue; }
                const ds = data[i].net_sent - data[i-1].net_sent;
                const dr = data[i].net_recv - data[i-1].net_recv;
                netSent.push(ds > 0 ? ds : 0);
                netRecv.push(dr > 0 ? dr : 0);
            }

            const netOpts = chartOpts('Bytes', false);
            netOpts.scales.y.ticks = { font: { size: 9 }, callback: v => formatNetRate(v) };
            netOpts.plugins.tooltip.callbacks = { label: ctx => ctx.dataset.label + ': ' + formatNetRate(ctx.parsed.y) };

            this.renderChart('netChart', {
                labels,
                datasets: [
                    { label: 'Sent', data: netSent, borderColor: '#f97316', backgroundColor: '#f9731620', borderWidth: 1.5, fill: true, pointRadius: 0, tension: 0.3 },
                    { label: 'Recv', data: netRecv, borderColor: '#06b6d4', backgroundColor: '#06b6d420', borderWidth: 1.5, fill: true, pointRadius: 0, tension: 0.3 }
                ]
            }, netOpts);
        },
        renderChart(id, chartData, opts) {
            const el = document.getElementById(id);
            if (!el) return;
            if (this.charts[id]) {
                this.charts[id].data = chartData;
                this.charts[id].options = opts;
                this.charts[id].update('none');
                return;
            }
            this.charts[id] = new Chart(el, { type: 'line', data: chartData, options: opts });
        }
    };
}

// Dashboard sparklines
function dashSparkline(el, hostId) {
    fetch('/api/v1/hosts/' + hostId + '/history?hours=1')
        .then(r => r.json())
        .then(data => {
            if (!data || !data.length || data.length < 2) return;
            new Chart(el, {
                type: 'line',
                data: {
                    labels: data.map(() => ''),
                    datasets: [{
                        data: data.map(p => p.cpu),
                        borderColor: '#22c55e80',
                        borderWidth: 1,
                        pointRadius: 0,
                        tension: 0.3,
                        fill: false
                    }]
                },
                options: {
                    responsive: true, maintainAspectRatio: false,
                    animation: false,
                    plugins: { legend: { display: false }, tooltip: { enabled: false } },
                    scales: { x: { display: false }, y: { display: false, min: 0, max: 100 } }
                }
            });
        })
        .catch(() => {});
}

// ---------- Chat markdown rendering (shared by sidebar panel and full-page chat) ----------

function renderMarkdown(text) {
    if (!text) return '';
    // Libraries failed to load (e.g. no internet): fall back to escaped text
    if (typeof marked === 'undefined' || typeof DOMPurify === 'undefined') {
        return text
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/`([^`]*)`/g, '<code class="bg-gray-700 px-1 rounded text-purple-300">$1</code>')
            .replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
            .replace(/\n/g, '<br>');
    }
    const tpl = document.createElement('template');
    tpl.innerHTML = DOMPurify.sanitize(marked.parse(text, { gfm: true, breaks: true }));

    tpl.content.querySelectorAll('a[href]').forEach(a => {
        a.setAttribute('target', '_blank');
        a.setAttribute('rel', 'noopener noreferrer');
    });
    tpl.content.querySelectorAll('table').forEach(t => {
        const wrap = document.createElement('div');
        wrap.className = 'md-table-wrap';
        t.parentNode.insertBefore(wrap, t);
        wrap.appendChild(t);
    });
    tpl.content.querySelectorAll('pre').forEach(pre => {
        const wrap = document.createElement('div');
        wrap.className = 'md-code';
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'md-copy';
        btn.title = 'Copy code';
        btn.innerHTML = '<i class="fa-regular fa-copy"></i>';
        pre.parentNode.insertBefore(wrap, pre);
        wrap.appendChild(btn);
        wrap.appendChild(pre);
    });
    return tpl.innerHTML;
}

// Copy buttons live in dynamically rendered messages, so delegate at document level
document.addEventListener('click', function(e) {
    const btn = e.target.closest('.md-copy');
    if (!btn || !navigator.clipboard) return;
    const code = btn.parentElement.querySelector('pre code');
    if (!code) return;
    navigator.clipboard.writeText(code.textContent).then(() => {
        btn.classList.add('copied');
        btn.innerHTML = '<i class="fa-solid fa-check"></i>';
        setTimeout(() => {
            btn.classList.remove('copied');
            btn.innerHTML = '<i class="fa-regular fa-copy"></i>';
        }, 1200);
    }).catch(() => {});
});
