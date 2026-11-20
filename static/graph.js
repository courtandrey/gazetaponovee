// Публичный Надзор: граф живёт в собственном окне, как карта. Колесо мыши или
// щипок двумя пальцами — масштаб вокруг указателя, перетаскивание — сдвиг,
// двойной щелчок — снова весь граф. Страница при этом не масштабируется.
// Узлы неподвижны; короткое нажатие на узел открывает участника.
(function () {
  "use strict";
  var svg = document.getElementById("graph-svg");
  if (!svg || !svg.viewBox || !svg.viewBox.baseVal) return;
  var box = svg.parentNode;

  var base = svg.viewBox.baseVal;
  var W = base.width, H = base.height;
  // вписываем не холст раскладки, а то, что на нём нарисовано, — как Cytoscape
  var g = { x: 0, y: 0, w: W, h: H };
  try {
    var bb = svg.getBBox();
    if (bb.width > 0 && bb.height > 0) g = { x: bb.x, y: bb.y, w: bb.width, h: bb.height };
  } catch (e) {}
  var PAD = 16;
  var KEY = "graph-view";

  // экран = k * граф + t
  var k = 1, tx = 0, ty = 0, cw = 0, ch = 0, fitK = 1;

  function clamp(v, lo, hi) { return Math.max(lo, Math.min(hi, v)); }
  function minK() { return fitK * 0.5; }
  function maxK() { return Math.max(fitK * 10, 3); }

  // центр окна не уходит за границы графа — граф нельзя «потерять»
  function bound() {
    k = clamp(k, minK(), maxK());
    tx = clamp(tx, cw / 2 - k * (g.x + g.w), cw / 2 - k * g.x);
    ty = clamp(ty, ch / 2 - k * (g.y + g.h), ch / 2 - k * g.y);
  }

  var frame = 0;
  function draw() {
    if (frame) return;
    frame = requestAnimationFrame(function () {
      frame = 0;
      svg.setAttribute("viewBox", (-tx / k) + " " + (-ty / k) + " " + (cw / k) + " " + (ch / k));
      try {
        sessionStorage.setItem(KEY, JSON.stringify({
          W: W, H: H, z: k / fitK, x: (cw / 2 - tx) / k, y: (ch / 2 - ty) / k
        }));
      } catch (e) {}
    });
  }

  function measure() {
    var r = box.getBoundingClientRect();
    cw = r.width || 1; ch = r.height || 1;
    fitK = Math.min((cw - 2 * PAD) / g.w, (ch - 2 * PAD) / g.h);
    if (!(fitK > 0)) fitK = Math.min(cw / g.w, ch / g.h);
    fitK = Math.min(fitK, 2); // пара узлов не должна раздуваться на весь экран
  }

  function fitView() {
    k = fitK;
    tx = cw / 2 - k * (g.x + g.w / 2);
    ty = ch / 2 - k * (g.y + g.h / 2);
  }

  // поставить точку графа (x, y) в центр окна с масштабом fitK * z
  function centerOn(x, y, z) {
    k = clamp(fitK * z, minK(), maxK());
    tx = cw / 2 - k * x;
    ty = ch / 2 - k * y;
    bound();
  }

  function local(cx, cy) {
    var r = box.getBoundingClientRect();
    return { x: cx - r.left, y: cy - r.top };
  }

  // масштаб в f раз, точка экрана (cx, cy) остаётся на месте
  function zoomAt(cx, cy, f) {
    var p = local(cx, cy);
    var nk = clamp(k * f, minK(), maxK());
    tx = p.x - (p.x - tx) * nk / k;
    ty = p.y - (p.y - ty) * nk / k;
    k = nk;
    bound();
    draw();
  }

  function panBy(dx, dy) {
    tx += dx; ty += dy;
    bound();
    draw();
  }

  var anim = 0;
  function animateTo(nk, ntx, nty) {
    cancelAnimationFrame(anim);
    var k0 = k, x0 = tx, y0 = ty, t0 = performance.now(), D = 350;
    (function step() {
      var t = Math.min(1, (performance.now() - t0) / D);
      var e = t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;
      k = k0 + (nk - k0) * e; tx = x0 + (ntx - x0) * e; ty = y0 + (nty - y0) * e;
      draw();
      if (t < 1) anim = requestAnimationFrame(step);
    })();
  }

  // начальный вид: прошлый (при переходе по узлу или вкладке) или весь граф
  measure();
  fitView();
  try {
    var s = JSON.parse(sessionStorage.getItem(KEY) || "null");
    if (s && s.W === W && s.H === H && s.z > 0) centerOn(s.x, s.y, s.z);
  } catch (e) {}
  draw();

  if (window.ResizeObserver) {
    new ResizeObserver(function () {
      var cx = (cw / 2 - tx) / k, cy = (ch / 2 - ty) / k, z = k / fitK;
      measure();
      centerOn(cx, cy, z);
      draw();
    }).observe(box);
  }

  svg.addEventListener("wheel", function (e) {
    e.preventDefault();
    cancelAnimationFrame(anim);
    var d = e.deltaY * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1);
    zoomAt(e.clientX, e.clientY, Math.exp(-d * (e.ctrlKey ? 0.01 : 0.002)));
  }, { passive: false });

  svg.addEventListener("dblclick", function (e) {
    e.preventDefault();
    animateTo(fitK, cw / 2 - fitK * (g.x + g.w / 2), ch / 2 - fitK * (g.y + g.h / 2));
  });

  // указатели: один — сдвиг, два — щипок
  var pts = new Map();
  var dragging = false, moved = 0;

  function pair() {
    var a = Array.from(pts.values());
    return { x: (a[0].x + a[1].x) / 2, y: (a[0].y + a[1].y) / 2, d: Math.hypot(a[0].x - a[1].x, a[0].y - a[1].y) };
  }

  svg.addEventListener("pointerdown", function (e) {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    cancelAnimationFrame(anim);
    pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pts.size === 1) moved = 0;
  });

  svg.addEventListener("pointermove", function (e) {
    var p = pts.get(e.pointerId);
    if (!p) return;
    var before = pts.size === 2 ? pair() : null;
    var dx = e.clientX - p.x, dy = e.clientY - p.y;
    p.x = e.clientX; p.y = e.clientY;
    moved += Math.abs(dx) + Math.abs(dy);
    if (!dragging && (moved > 6 || pts.size > 1)) {
      dragging = true;
      svg.classList.add("dragging");
      pts.forEach(function (_, id) { try { svg.setPointerCapture(id); } catch (err) {} });
    }
    if (!dragging) return;
    if (before) {
      var after = pair();
      if (before.d > 0) zoomAt(after.x, after.y, after.d / before.d);
      panBy(after.x - before.x, after.y - before.y);
    } else if (pts.size === 1) {
      panBy(dx, dy);
    }
  });

  function stop(ev) { ev.preventDefault(); ev.stopPropagation(); }
  function up(e) {
    if (!pts.delete(e.pointerId)) return;
    if (pts.size === 0 && dragging) {
      dragging = false;
      svg.classList.remove("dragging");
      // перетаскивание не должно заканчиваться переходом по ссылке узла
      svg.addEventListener("click", stop, { capture: true, once: true });
      setTimeout(function () { svg.removeEventListener("click", stop, true); }, 0);
    }
  }
  svg.addEventListener("pointerup", up);
  svg.addEventListener("pointercancel", up);
})();
