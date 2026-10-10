// Aegis marketing site: marine snow, scroll reveals, and a jellyfish
// that keeps an eye on you. No dependencies.
(function () {
  document.documentElement.classList.remove("no-js");
  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // Marine snow drifting down through the water column.
  var snow = document.getElementById("snow");
  if (snow && !reduced) {
    var count = Math.min(70, Math.round(window.innerWidth / 18));
    for (var i = 0; i < count; i++) {
      var f = document.createElement("span");
      var size = Math.random() * 2.6 + 0.8;
      f.className = "flake";
      f.style.width = f.style.height = size + "px";
      f.style.left = Math.random() * 100 + "vw";
      f.style.opacity = (Math.random() * 0.5 + 0.2).toFixed(2);
      f.style.animationDuration = (Math.random() * 24 + 18).toFixed(1) + "s";
      f.style.animationDelay = (-Math.random() * 40).toFixed(1) + "s";
      f.style.setProperty("--dx", (Math.random() * 80 - 40).toFixed(0) + "px");
      snow.appendChild(f);
    }
  }

  // Fade sections in as you descend.
  var reveals = document.querySelectorAll(".reveal");
  if ("IntersectionObserver" in window && !reduced) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) { e.target.classList.add("in"); io.unobserve(e.target); }
      });
    }, { threshold: 0.12 });
    reveals.forEach(function (el) { io.observe(el); });
  } else {
    reveals.forEach(function (el) { el.classList.add("in"); });
  }

  // The hero jellyfish watches your cursor, the way Aegis watches agents.
  var jelly = document.getElementById("hero-jelly");
  if (!jelly) return;
  var pupils = jelly.querySelectorAll(".pupil");
  var caption = document.getElementById("jelly-caption");
  var defaultCaption = caption ? caption.textContent : "";

  if (!reduced) {
    window.addEventListener("pointermove", function (ev) {
      var r = jelly.getBoundingClientRect();
      var cx = r.left + r.width * 0.5;
      var cy = r.top + r.height * 0.375;
      var dx = ev.clientX - cx;
      var dy = ev.clientY - cy;
      var d = Math.hypot(dx, dy) || 1;
      var k = Math.min(1, d / 300) * 2.4;
      var tx = (dx / d) * k;
      var ty = (dy / d) * k;
      pupils.forEach(function (p) { p.style.transform = "translate(" + tx + "px," + ty + "px)"; });
    }, { passive: true });
  }

  // Poke it and it stings, gently.
  var stings = [
    "403 · player: none",
    "403 · that's not yours to read",
    "freebusy only, friend",
    "tokens? what tokens?",
    "logged to the Map ✓"
  ];
  var n = 0, timer;
  jelly.addEventListener("click", function () {
    jelly.classList.add("stung");
    if (caption) caption.textContent = "⚡ " + stings[n++ % stings.length];
    clearTimeout(timer);
    timer = setTimeout(function () {
      jelly.classList.remove("stung");
      if (caption) caption.textContent = defaultCaption;
    }, 1600);
  });
})();
