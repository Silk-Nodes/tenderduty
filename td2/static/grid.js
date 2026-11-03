// Block grid: one row per chain, one cell per block, newest on the left.
const cellH = 24
const cellW = 9
const labelW = 120

const palette = {
    dark: {text: "#8f969f", line: "#2c3138", signed: ["rgba(255,255,255,0.07)", "rgba(255,255,255,0.035)"], edge: "rgba(255,255,255,0.22)"},
    light: {text: "#646b74", line: "#dde0e5", signed: ["rgba(28,31,36,0.10)", "rgba(28,31,36,0.05)"], edge: "rgba(28,31,36,0.28)"},
}
const kinds = [
    {code: 4, label: "proposer", stops: [[0, "#2bb673"], [0.7, "#4fd68f"], [1, "#9ae8bd"]]},
    {code: 3, label: "signed", stops: null},
    {code: 2, label: "miss/precommit", stops: [[0, "#3b8be0"], [1, "#3b8be0"]]},
    {code: 1, label: "miss/prevote", stops: [[0, "#8b5cf6"], [1, "#8b5cf6"]]},
    {code: 0, label: "missed", stops: [[0, "#e5534b"]], strike: true},
    {code: -1, label: "no data", stops: [[0, "rgba(127,127,127,0.25)"]]},
]
const kindByCode = Object.fromEntries(kinds.map(k => [k.code, k]))

let isDark = true
let lastDrawn = null

function theme() { return isDark ? palette.dark : palette.light }

function initTheme() {
    // ?theme=dark or ?theme=light in the address pins the theme, otherwise the last choice or the system setting
    let saved = new URLSearchParams(location.search).get("theme")
    try { saved = saved || localStorage.getItem("theme") } catch (e) {}
    isDark = saved ? saved === "dark" : !window.matchMedia("(prefers-color-scheme: light)").matches
    applyTheme()
    const wrap = document.getElementById("gridWrap")
    new ResizeObserver(() => { if (lastDrawn) drawSeries(lastDrawn) }).observe(wrap)
    const canvas = document.getElementById("canvas")
    canvas.addEventListener("mousemove", showTip)
    canvas.addEventListener("mouseleave", () => { document.getElementById("gridTip").style.display = "none" })
}

function toggleTheme() {
    isDark = !isDark
    try { localStorage.setItem("theme", isDark ? "dark" : "light") } catch (e) {}
    applyTheme()
}

function applyTheme() {
    document.documentElement.dataset.theme = isDark ? "dark" : "light"
    document.body.className = isDark ? "uk-light" : ""
    document.documentElement.style.colorScheme = isDark ? "dark" : "light"
    legend()
    if (lastDrawn) drawSeries(lastDrawn)
}

// sizeCanvas gives the canvas its CSS size and a matching backing store, scaled once for the device pixel ratio.
function sizeCanvas(canvas, cssW, cssH) {
    const dpr = window.devicePixelRatio || 1
    canvas.style.width = cssW + "px"
    canvas.style.height = cssH + "px"
    canvas.width = Math.round(cssW * dpr)
    canvas.height = Math.round(cssH * dpr)
    const ctx = canvas.getContext("2d")
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    return ctx
}

// each cell leaves a one pixel gap on its right, so a run of the same result still reads as separate blocks
function fillCell(ctx, kind, x, y, row) {
    const grad = ctx.createLinearGradient(x, y, x + cellW - 1, y)
    if (kind.code === 3) {
        grad.addColorStop(0, theme().signed[row % 2])
        grad.addColorStop(0.9, theme().signed[row % 2])
        grad.addColorStop(1, theme().edge)
    } else {
        for (const [at, color] of kind.stops) grad.addColorStop(at, color)
    }
    ctx.fillStyle = grad
    ctx.fillRect(x, y, cellW - 1, cellH)
    if (kind.strike) {
        ctx.beginPath()
        ctx.moveTo(x + 1 + cellW / 4, y + cellH / 2)
        ctx.lineTo(x + cellW - cellW / 4 - 1, y + cellH / 2)
        ctx.strokeStyle = "white"
        ctx.stroke()
    }
}

function legend() {
    const canvas = document.getElementById("legend")
    const ctx = sizeCanvas(canvas, 700, cellH + 4)
    ctx.font = "12px -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"
    let x = 0
    for (const kind of kinds) {
        fillCell(ctx, kind, x, 2, 0)
        x += cellW * 1.5
        ctx.fillStyle = theme().text
        ctx.fillText(kind.label, x, 2 + cellH / 1.3)
        x += ctx.measureText(kind.label).width + 18
    }
}

function drawSeries(states) {
    lastDrawn = states
    const canvas = document.getElementById("canvas")
    const wrap = document.getElementById("gridWrap")
    const cssW = Math.max(wrap.clientWidth, labelW + cellW * 20)
    const columns = Math.floor((cssW - labelW) / cellW)
    const rows = states.Status.length
    const ctx = sizeCanvas(canvas, cssW, cellH * rows + 2)
    for (let j = 0; j < rows; j++) {
        const y = cellH * j + 2
        ctx.font = "600 13px -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"
        ctx.fillStyle = theme().text
        ctx.fillText(states.Status[j].name, 5, y + cellH - 6, labelW - 5)
        const blocks = states.Status[j].blocks
        for (let i = 0; i < Math.min(columns, blocks.length); i++) {
            const kind = kindByCode[blocks[i]] || kindByCode[-1]
            fillCell(ctx, kind, labelW + i * cellW, y, j)
        }
        if (j > 0) {
            ctx.beginPath()
            ctx.moveTo(labelW, y - 0.5)
            ctx.lineTo(cssW, y - 0.5)
            ctx.strokeStyle = theme().line
            ctx.stroke()
        }
    }
}

// showTip names the chain, the approximate height and the result of the cell under the pointer. The height is
// an estimate: gaps while reconnecting are not back-filled, so older cells can be off by the gap.
function showTip(e) {
    const tip = document.getElementById("gridTip")
    if (!lastDrawn) return
    const rect = e.target.getBoundingClientRect()
    const x = e.clientX - rect.left, y = e.clientY - rect.top
    const row = Math.floor((y - 2) / cellH)
    const col = Math.floor((x - labelW) / cellW)
    const chain = lastDrawn.Status[row]
    if (!chain || col < 0 || col >= chain.blocks.length) { tip.style.display = "none"; return }
    const kind = kindByCode[chain.blocks[col]] || kindByCode[-1]
    const height = chain.height ? ` ~${(chain.height - col).toLocaleString()}` : ""
    tip.textContent = `${chain.name}${height}: ${kind.label}`
    tip.style.display = "block"
    tip.style.left = Math.min(x + 12, rect.width - 220) + "px"
    tip.style.top = (y + 14) + "px"
}
