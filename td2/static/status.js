// Dashboard state: the last status message, which rows are expanded, and the log lines.
document.addEventListener("DOMContentLoaded", () => {
    document.getElementById("themeToggle").addEventListener("click", toggleTheme)
    initTheme()
    loadState()
})
let latest = null
const expanded = new Set(new URLSearchParams(location.search).getAll("open")) // ?open=Chain opens that chain's panel
let logs = []
const esc = (s) => String(s).replace(/[&<>"']/g, c => ({"&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"}[c]))
const num = (n) => Number(n).toLocaleString()

async function getJSON(path) {
    try {
        const r = await fetch(path, {cache: 'no-cache'})
        return await r.json()
    } catch (e) {
        console.log(path, e)
        return null
    }
}

async function loadState() {
    const showLog = await getJSON("logsenabled")
    if (showLog && showLog.enabled === false) {
        document.getElementById("logContainer").hidden = true
    }
    const state = await getJSON("state")
    if (state && state.Status) {
        latest = state
        render(state)
    }
    await loadLogs()
    connect()
    setInterval(tickAges, 1000)
    document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "visible" && latest) render(latest)
    })
}

async function loadLogs() {
    const lines = await getJSON("logs")
    if (!lines) return
    logs = []
    for (let i = lines.length - 1; i >= 0; i--) {
        if (lines[i].ts !== 0) addLogMsg(lines[i])
    }
}

function when(ts) {
    return new Date(ts * 1000).toLocaleString()
}

// shortWhen drops the year and seconds, enough for a panel entry
function shortWhen(ts) {
    return new Date(ts * 1000).toLocaleString(undefined, {month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit"})
}

function ago(ts) {
    const s = Math.max(0, Math.floor(Date.now() / 1000 - ts))
    if (s < 60) return `${s}s ago`
    if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s ago`
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m ago`
}

function duration(s) {
    if (s < 0) return "eta unknown"
    if (s < 3600) return `${Math.floor(s / 60)}m`
    if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
    return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`
}

// ageClass colours the last-block cell: orange after a minute, red once past the chain's stalled threshold.
function ageClass(c) {
    if (!c.last_block) return "muted"
    const s = Date.now() / 1000 - c.last_block
    if (c.stalled_minutes > 0 && s > c.stalled_minutes * 60) return "bad"
    if (s > 60) return "warn"
    return "muted"
}

function tickAges() {
    if (!latest || document.visibilityState === "hidden") return
    for (const c of latest.Status) {
        const el = document.getElementById(`age-${c.name}`)
        if (el && c.last_block) {
            el.textContent = ago(c.last_block)
            el.className = "mono " + ageClass(c)
        }
    }
}

const na = `<span class="muted">n/a</span>`

// healthDot is the left-hand indicator: red for a state that needs a hand now, amber for any open alert, grey while unknown.
function healthDot(c) {
    if (c.validator && c.moniker === "not connected") return `<span class="st off" uk-tooltip="not connected yet"></span>`
    if (c.jailed || c.tombstoned || ageClass(c) === "bad") return `<span class="st bad"></span>`
    if (c.active_alerts > 0) return `<span class="st warn" uk-tooltip="${esc(c.active_alerts)} active alert${c.active_alerts > 1 ? "s" : ""}"></span>`
    return `<span class="st"></span>`
}

function statusCell(c) {
    if (!c.validator) return na
    if (c.moniker === "not connected") return `<span class="warn">unknown</span>`
    if (c.tombstoned) return `<span class="bad">Tombstoned</span>`
    if (c.jailed) return `<span class="bad">Jailed</span>`
    if (c.bonded) return `<span class="ok">Active</span>`
    return `<span class="muted">Not active</span>`
}

// uptimeCell shows how much of the jail allowance is used: missed blocks against the count that gets the
// validator jailed, with the signed percentage of the window alongside.
function uptimeCell(c) {
    if (!c.validator || c.window === 0) return na
    const pct = 100 - (c.missed / c.window) * 100
    if (!c.jail_after) {
        return `<span class="uptime"><span class="mono muted">${num(c.missed)} / ${num(c.window)}</span><span class="pct">${pct.toFixed(2)}%</span></span>`
    }
    const used = Math.min(1, c.missed / c.jail_after)
    const cls = used >= 0.5 ? "bad" : used >= 0.2 ? "warn" : ""
    // hue runs from green (140) through yellow and orange to red (0) as the allowance is used up
    const colour = `hsl(${Math.round(140 * (1 - used))} 70% 45%)`
    const minSigned = (100 * (c.window - c.jail_after) / c.window).toFixed(0)
    const tip = `window ${num(c.window)} blocks, must sign ${minSigned}%, jailed after ${num(c.jail_after)} missed`
    return `<div uk-tooltip="${esc(tip)}">
        <span class="uptime"><span class="mono ${cls || "muted"}">${num(c.missed)} / ${num(c.jail_after)}</span><span class="pct ${cls}">${pct.toFixed(2)}%</span></span>
        <div class="bar"><i style="width: ${(100 * used).toFixed(1)}%; background: ${colour}"></i></div></div>`
}

function upgradeCell(c) {
    if (!c.upgrade) {
        // an upgrade proposal still being voted on: the plan is not scheduled yet, but the height is known
        const p = (c.proposals || []).filter(p => p.upgrade_height).sort((a, b) => a.upgrade_height - b.upgrade_height)[0]
        if (p) return `<span class="muted" uk-tooltip="proposal #${esc(p.id)}, vote ends ${esc(shortWhen(p.ends))}">${esc(p.upgrade_name)} @ ${num(p.upgrade_height)} proposed</span>`
        return `<span class="muted">None</span>`
    }
    const u = c.upgrade
    let cls = ""
    if (u.eta_seconds >= 0 && u.eta_seconds < 3600) cls = "bad"
    else if (u.eta_seconds >= 0 && u.eta_seconds < 86400) cls = "warn"
    const blocks = u.height - c.height
    return `<span class="${cls}" uk-tooltip="${esc(num(blocks))} blocks to go">${esc(u.name)} @ ${num(u.height)} in ${duration(u.eta_seconds)}</span>`
}

function govCell(c) {
    if (!c.validator) return na
    const open = (c.proposals || [])
    const unvoted = open.filter(p => !p.voted)
    if (open.length === 0) return `<span class="muted">0</span>`
    const tip = open.map(p => `#${p.id} ${p.voted ? "voted" : "NOT voted"}${p.is_upgrade ? " (upgrade)" : ""}`).join("; ")
    const cls = unvoted.length > 0 ? "warn" : "ok"
    return `<span class="badge ${cls}" uk-tooltip="${esc(tip)}">${unvoted.length}/${open.length}</span>`
}

function nodesCell(c) {
    const tip = (c.node_status || []).map(n =>
        `${n.name}: ${n.up ? "up" : "down"}${n.active ? ", in use" : ""}${n.height ? ", height " + n.height : ""}${n.lag ? ", " + n.lag + " behind" : ""}`
    ).join("; ")
    const cls = c.healthy_nodes < c.nodes ? "warn" : ""
    const icon = c.healthy_nodes < c.nodes ? `<span uk-icon="icon: arrow-down; ratio: 0.8"></span> ` : ""
    return `<span class="${cls}" title="${esc(tip)}">${icon}${esc(c.healthy_nodes)} / ${esc(c.nodes)}</span>`
}

// nodeLink turns an RPC URL into a link so a dead node can be checked in a new tab.
function nodeLink(name) {
    if (!/^(https?|tcp|wss?):\/\//.test(name)) return esc(name)
    const href = name.replace(/^(tcp|ws):/, "http:").replace(/^wss:/, "https:")
    return `<a href="${esc(href)}" target="_blank" rel="noopener">${esc(name)}</a>`
}

function detailPanel(c) {
    let html = `<div class="uk-grid-small" uk-grid>`
    html += `<div class="uk-width-1-2@m"><h4>Active alerts</h4>`
    if ((c.alerts || []).length === 0) {
        html += `<div class="muted">none</div>`
    } else {
        html += `<table class="uk-table uk-table-small">`
        for (const a of c.alerts) {
            html += `<tr><td class="mono muted" style="white-space: nowrap" uk-tooltip="${esc(when(a.since))}">${esc(shortWhen(a.since))}</td><td>${esc(a.message)}</td></tr>`
        }
        html += `</table>`
    }
    if (c.last_error) {
        html += `<h4>Last event</h4><div class="mono">${esc(c.last_error)}</div>`
    }
    html += `</div><div class="uk-width-1-2@m"><h4>RPC nodes</h4>`
    html += `<table class="uk-table uk-table-small"><tr class="muted"><th>node</th><th>state</th><th>height</th><th>lag</th><th>note</th></tr>`
    for (const n of (c.node_status || [])) {
        const state = n.up ? `<span class="ok">up</span>` : (n.syncing ? `<span class="warn">syncing</span>` : `<span class="bad">down</span>`)
        html += `<tr><td class="mono">${nodeLink(n.name)}${n.active ? ' <span class="muted">(in use)</span>' : ""}</td><td>${state}</td><td class="mono">${n.height ? num(n.height) : ""}</td><td class="mono">${n.lag || ""}</td><td class="muted note" title="${esc(n.message || "")}">${esc(n.message || "")}</td></tr>`
    }
    html += `</table>`
    if ((c.proposals || []).length > 0) {
        html += `<h4>Proposals in voting</h4><table class="uk-table uk-table-small">`
        for (const p of c.proposals) {
            html += `<tr><td class="mono">#${p.id}</td><td>${esc(p.title)}${p.is_upgrade ? ' <span class="warn">upgrade</span>' : ""}</td><td>${p.voted ? '<span class="ok">voted</span>' : '<span class="warn">not voted</span>'}</td><td class="muted mono">ends ${esc(when(p.ends))}</td></tr>`
        }
        html += `</table>`
    }
    html += `</div></div>`
    return html
}

function summary(chains) {
    let nodes = 0, up = 0, alerts = 0
    for (const c of chains) { nodes += c.nodes; up += c.healthy_nodes; alerts += c.active_alerts }
    document.getElementById("sumChains").innerHTML = `<b>${chains.length}</b> chain${chains.length === 1 ? "" : "s"}`
    const n = document.getElementById("sumNodes")
    n.innerHTML = `<b>${up} / ${nodes}</b> nodes up`
    n.classList.toggle("short", up < nodes)
    const a = document.getElementById("sumAlerts")
    a.innerHTML = `<b>${alerts}</b> alert${alerts === 1 ? "" : "s"}`
    a.classList.toggle("none", alerts === 0)
    return alerts
}

const heights = new Map()
const rows = new Map() // chain name -> {row, cells, detail, detailHtml}, kept so an update only touches what changed
const cellClass = ["", "name", "muted mono", "mono", "", "uk-text-truncate", "", "jail", "uk-text-right mono", "", "uk-text-center", "uk-text-center"]

// place puts node at position pos in body without moving it when it is already there, so hover state and
// open tooltips survive the update
function place(body, node, pos) {
    if (body.children[pos] !== node) body.insertBefore(node, body.children[pos] || null)
}

function render(status) {
    // validators first, then chains monitored for their nodes only; within each group alerts first, then name
    const chains = [...status.Status].sort((a, b) =>
        b.validator - a.validator || (b.active_alerts > 0) - (a.active_alerts > 0) || a.name.localeCompare(b.name))
    const body = document.getElementById("statusTable")
    let divider = body.querySelector("tr.divider")
    let dividerDone = false
    let pos = 0
    const seen = new Set()
    for (const c of chains) {
        if (!c.validator && !dividerDone) {
            dividerDone = true
            if (!divider) {
                divider = document.createElement("tr")
                divider.className = "divider"
                divider.innerHTML = `<td colspan="12">RPC only</td>`
            }
            place(body, divider, pos++)
        }
        const moved = heights.get(c.name) !== c.height
        heights.set(c.name, c.height)
        const cells = [
            healthDot(c),
            esc(c.name),
            esc(c.chain_id),
            `<span class="${moved ? "uk-animation-scale-up" : ""}">${c.height ? num(c.height) : ""}</span>`,
            `<span id="age-${esc(c.name)}" class="mono ${ageClass(c)}">${c.last_block ? ago(c.last_block) : "no blocks yet"}</span>`,
            !c.validator ? `<span class="muted">RPC only</span>` : c.moniker === "not connected" ? `<span class="warn">not connected</span>` : esc(c.moniker),
            statusCell(c),
            uptimeCell(c),
            c.tokens ? c.voting_power_pct.toFixed(2) + "%" : na,
            upgradeCell(c),
            govCell(c),
            nodesCell(c),
        ]
        let r = rows.get(c.name)
        if (!r) {
            const row = document.createElement("tr")
            cellClass.forEach(cls => { row.insertCell().className = cls })
            row.cells[5].style.maxWidth = "16em"
            row.onclick = () => { expanded.has(c.name) ? expanded.delete(c.name) : expanded.add(c.name); render(latest) }
            r = {row, cells: [], detail: null, detailHtml: ""}
            rows.set(c.name, r)
        }
        r.row.className = "chain" + (expanded.has(c.name) ? " open" : "")
        cells.forEach((html, i) => { if (r.cells[i] !== html) r.row.cells[i].innerHTML = html })
        r.cells = cells
        place(body, r.row, pos++)
        if (expanded.has(c.name)) {
            if (!r.detail) {
                r.detail = document.createElement("tr")
                r.detail.className = "detail"
                r.detail.innerHTML = `<td colspan="12"></td>`
                r.detailHtml = ""
            }
            const html = detailPanel(c)
            if (html !== r.detailHtml) { r.detail.firstElementChild.innerHTML = html; r.detailHtml = html }
            place(body, r.detail, pos++)
        } else if (r.detail) {
            r.detail.remove()
            r.detail = null
        }
        seen.add(c.name)
    }
    for (const [name, r] of rows) {
        if (!seen.has(name)) { r.row.remove(); if (r.detail) r.detail.remove(); rows.delete(name) }
    }
    if (!dividerDone && divider) divider.remove()
    const alerts = summary(chains)
    document.title = alerts > 0 ? `(${alerts}) Tenderduty` : "Tenderduty"
    // the block grid is about signing, so only validators are drawn
    drawSeries({Status: chains.filter(c => c.validator)})
}

function addLogMsg(m) {
    logs.unshift(`${new Date(m.ts * 1000).toLocaleString()}  ${m.msg}`)
    if (logs.length > 256) logs.pop()
    if (document.visibilityState !== "hidden") {
        document.getElementById("logs").innerText = logs.join("\n")
    }
}

function setConnected(on) {
    document.getElementById("connDot").className = on ? "dot on" : "dot"
    document.getElementById("connText").textContent = on ? "live" : "reconnecting"
}

let retries = 0
function connect() {
    const proto = location.protocol === "https:" ? "wss://" : "ws://"
    const socket = new WebSocket(proto + location.host + "/ws")
    socket.onopen = async () => {
        setConnected(true)
        if (retries > 0) {
            // catch up on what was missed while disconnected
            const state = await getJSON("state")
            if (state && state.Status) { latest = state; render(state) }
            await loadLogs()
        }
        retries = 0
    }
    socket.onmessage = (event) => {
        const msg = JSON.parse(event.data)
        if (msg.msgType === "log") {
            addLogMsg(msg)
        } else if (msg.msgType === "update") {
            latest = msg
            if (document.visibilityState !== "hidden") render(msg)
        }
    }
    socket.onclose = () => {
        setConnected(false)
        const wait = Math.min(30000, 1000 * 2 ** retries)
        retries++
        setTimeout(connect, wait)
    }
}
