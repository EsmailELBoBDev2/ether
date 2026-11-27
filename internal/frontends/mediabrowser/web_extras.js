// Ether extras for the Emby Web and Jellyfin Web apps Ether serves. Ether is the server, so
// the server's admin pages live in the Ether console: those routes open the
// matching console page instead. Ether also adds its own screensaver. The
// app's code is untouched; this file only reacts to route changes and idle
// time.
(function () {
  "use strict";
  var ADMIN = [
    // Jellyfin Web: everything under #/dashboard.
    [/^\/dashboard\/users(\b|$)/, "/viewers"],
    [/^\/dashboard\/libraries(\b|$)/, "/libraries"],
    [/^\/dashboard\/playback(\b|$)/, "/playback"],
    [/^\/dashboard\/livetv(\b|$)/, "/livetv"],
    [/^\/dashboard\/plugins(\b|$)/, "/plugins"],
    [/^\/dashboard\/(logs|tasks|backups)(\b|$)/, "/maintenance"],
    [/^\/dashboard\/(devices|activity)(\b|$)/, "/activity"],
    [/^\/dashboard\/keys(\b|$)/, "/apps/APP"],
    // Emby Web.
    [/^\/(dashboard|dashboard\.html|dashboard\/settings|dashboard\/releasenotes\.html|network)(\b|$)/, "/server"],
    [/^\/devices(\b|$)/, "/activity"],
    [/^\/serveractivity(\b|$)/, "/activity"],
    [/^\/transcoding(\b|$)/, "/playback"],
    [/^\/librarysetup(\b|$)/, "/libraries"],
    [/^\/livetvsetup(\b|$)/, "/livetv"],
    [/^\/(logs|log|scheduledtasks|scheduledtask|database)(\b|$)/, "/maintenance"],
    [/^\/(plugins|configurationpage)(\b|$)/, "/plugins"],
    [/^\/apikeys(\b|$)/, "/apps/APP"],
    [/^\/users(\b|$)/, "/viewers"],
  ];
  var config = null;

  function api() {
    return globalThis.ApiClient && globalThis.ApiClient.accessToken && globalThis.ApiClient.accessToken() ? globalThis.ApiClient : null;
  }

  function loadConfig() {
    var c = api();
    if (!c) return Promise.resolve(null);
    if (config) return Promise.resolve(config);
    return c.getJSON(c.getUrl("Ether/WebExtras")).then(function (v) {
      config = v || {};
      return config;
    }, function () {
      return null;
    });
  }

  // Emby Web routes are #!/page, Jellyfin Web's #/page.
  function route() {
    var h = location.hash || "";
    if (h.indexOf("#!") === 0) h = h.slice(2);
    else if (h.indexOf("#") === 0) h = h.slice(1);
    else return "";
    return h.split("?")[0];
  }

  function checkAdminRoute() {
    var r = route();
    for (var n = 0; n < ADMIN.length; n++) {
      if (ADMIN[n][0].test(r)) {
        var target = ADMIN[n][1];
        loadConfig().then(function (c) {
          if (!c || !c.console) return;
          // Same tab: a new window from a timer is a blocked popup.
          location.assign(c.console.replace(/\/$/, "") + target.replace("APP", c.app || "emby"));
        });
        return;
      }
    }
  }

  // Screensaver: after the owner-set idle time, backdrops from the viewer's
  // own libraries with the title and a clock. Never while a video plays.
  var idleTimer = 0;
  var overlay = null;
  var slideTimer = 0;

  function playing() {
    var v = document.querySelector("video");
    return !!(v && !v.paused && !v.ended);
  }

  function dismiss() {
    if (overlay) {
      overlay.remove();
      overlay = null;
    }
    clearInterval(slideTimer);
    armIdle();
  }

  function startSaver() {
    var c = api();
    if (!c || overlay || playing() || document.hidden) {
      armIdle();
      return;
    }
    c.getJSON(
      c.getUrl("Users/" + c.getCurrentUserId() + "/Items", {
        Recursive: true,
        IncludeItemTypes: "Movie,Series",
        ImageTypes: "Backdrop",
        SortBy: "Random",
        Limit: 40,
        Fields: "BackdropImageTags,ProductionYear",
        EnableTotalRecordCount: false,
      })
    ).then(function (res) {
      var items = ((res && res.Items) || []).filter(function (i) {
        return i.BackdropImageTags && i.BackdropImageTags.length;
      });
      if (!items.length || overlay || playing()) return;
      overlay = document.createElement("div");
      overlay.setAttribute("data-ether-screensaver", "");
      overlay.style.cssText =
        "position:fixed;inset:0;z-index:2147483646;background:#000;color:#fff;cursor:none;font-family:inherit;";
      var img = document.createElement("div");
      img.style.cssText = "position:absolute;inset:0;background:center/cover no-repeat;transition:opacity 1.2s;";
      var shade = document.createElement("div");
      shade.style.cssText = "position:absolute;inset:0;background:linear-gradient(transparent 55%,rgba(0,0,0,.75));";
      var title = document.createElement("div");
      title.style.cssText = "position:absolute;left:4vw;bottom:5vh;font-size:3.2vh;font-weight:600;text-shadow:0 2px 8px #000;";
      var clock = document.createElement("div");
      clock.style.cssText = "position:absolute;right:4vw;bottom:5vh;font-size:5vh;font-weight:300;text-shadow:0 2px 8px #000;";
      overlay.append(img, shade, title, clock);
      document.body.appendChild(overlay);
      var n = 0;
      var show = function () {
        var it = items[n++ % items.length];
        img.style.opacity = "0";
        setTimeout(function () {
          img.style.backgroundImage =
            "url('" + c.getImageUrl(it.Id, { type: "Backdrop", tag: it.BackdropImageTags[0], maxWidth: 1920 }) + "')";
          img.style.opacity = "1";
          title.textContent = it.Name + (it.ProductionYear ? " (" + it.ProductionYear + ")" : "");
        }, 600);
        clock.textContent = new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
      };
      show();
      slideTimer = setInterval(show, 12000);
    }, armIdle);
  }

  function armIdle() {
    clearTimeout(idleTimer);
    if (!config || !config.screensaverMinutes) return;
    idleTimer = setTimeout(startSaver, config.screensaverMinutes * 60000);
  }

  ["mousemove", "mousedown", "keydown", "touchstart", "wheel"].forEach(function (ev) {
    window.addEventListener(
      ev,
      function () {
        if (overlay) dismiss();
        else armIdle();
      },
      { capture: true, passive: true }
    );
  });

  // TV mode: Emby Web's own TV layout makes playback a Premiere feature, so
  // Ether keeps the regular layout (playback free) and makes it usable from
  // a sofa: larger type, no cursor, and arrow-key / remote navigation.
  // On by default in TV browsers; ?tv=1 or ?tv=0 in the address sets it for
  // this browser.
  function tvWanted() {
    var q = /[?&]tv=([01])/.exec(location.search + location.hash);
    try {
      if (q) localStorage.setItem("ether-tv-mode", q[1]);
      var saved = localStorage.getItem("ether-tv-mode");
      if (saved === "1" || saved === "0") return saved === "1";
    } catch (e) {}
    return /Android TV|AFT|BRAVIA|SMART-TV|SmartTV|Tizen|Web0S|webOS|NetCast|Roku|CrKey|GoogleTV|HbbTV/i.test(navigator.userAgent);
  }

  function startTvMode() {
    var style = document.createElement("style");
    style.setAttribute("data-ether-tv", "");
    style.textContent =
      "html{font-size:150%!important;cursor:none!important}" +
      "*{cursor:none!important}" +
      "[data-ether-tv-focus]{outline:.25em solid var(--theme-accent-text-color,#29b6f6)!important;outline-offset:.15em!important;border-radius:.3em;transform:scale(1.04);transition:transform .12s;z-index:2;position:relative}";
    document.head.appendChild(style);

    var SELECTOR = "button:not([disabled]),a[href],input,select,textarea,[tabindex]:not([tabindex='-1']),.card,.listItem";
    var current = null;

    function visible(el) {
      var r = el.getBoundingClientRect();
      if (r.width < 4 || r.height < 4) return false;
      var cs = getComputedStyle(el);
      return cs.visibility !== "hidden" && cs.display !== "none" && r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth;
    }

    function candidates() {
      var root = document.querySelector(".dialogContainer .dialog:not(.hide)") || document.querySelector(".view:not(.hide)") || document.body;
      var head = document.querySelector(".skinHeader");
      var list = Array.prototype.slice.call(root.querySelectorAll(SELECTOR));
      if (head && root !== document.body && !root.closest(".dialogContainer")) list = list.concat(Array.prototype.slice.call(head.querySelectorAll(SELECTOR)));
      return list.filter(function (el) {
        // A card is one stop: not its select box or overlay buttons.
        var card = el.closest(".card");
        if (card && card !== el) return false;
        return !el.closest("[data-ether-screensaver]") && visible(el);
      });
    }

    function focus(el) {
      if (current) current.removeAttribute("data-ether-tv-focus");
      current = el;
      if (!el) return;
      el.setAttribute("data-ether-tv-focus", "");
      if (typeof el.focus === "function") el.focus({ preventScroll: true });
      el.scrollIntoView({ block: "nearest", inline: "nearest", behavior: "smooth" });
    }

    function move(dir) {
      var list = candidates();
      if (!list.length) return;
      if (!current || !document.contains(current) || list.indexOf(current) < 0) {
        focus(list[0]);
        return;
      }
      var a = current.getBoundingClientRect();
      var best = null, bestScore = Infinity;
      // Edge-to-edge distance along the move, and the gap across it (0 when
      // the two overlap), so the nearest row or column wins.
      function gap(lo1, hi1, lo2, hi2) {
        return Math.max(0, Math.max(lo1, lo2) - Math.min(hi1, hi2));
      }
      list.forEach(function (el) {
        if (el === current || current.contains(el) || el.contains(current)) return;
        var b = el.getBoundingClientRect();
        var primary, cross;
        if (dir === "left") { primary = a.left - b.right; cross = gap(a.top, a.bottom, b.top, b.bottom); }
        else if (dir === "right") { primary = b.left - a.right; cross = gap(a.top, a.bottom, b.top, b.bottom); }
        else if (dir === "up") { primary = a.top - b.bottom; cross = gap(a.left, a.right, b.left, b.right); }
        else { primary = b.top - a.bottom; cross = gap(a.left, a.right, b.left, b.right); }
        if (primary < -2) return;
        var score = Math.max(primary, 0) + cross * 3;
        if (score < bestScore) { bestScore = score; best = el; }
      });
      if (best) focus(best);
    }

    var KEYS = { ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down", Left: "left", Right: "right", Up: "up", Down: "down" };
    window.addEventListener(
      "keydown",
      function (e) {
        var tag = (document.activeElement && document.activeElement.tagName) || "";
        var typing = tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
        if (document.querySelector("video") && !document.querySelector("video").paused && !document.querySelector(".videoOsdBottom:not(.hide)")) return;
        if (KEYS[e.key] && !typing) {
          e.preventDefault();
          e.stopPropagation();
          move(KEYS[e.key]);
        } else if ((e.key === "Enter" || e.key === "OK") && current && document.contains(current) && !typing) {
          e.preventDefault();
          e.stopPropagation();
          // Emby cards open through their inner action button.
          var action = current.classList.contains("card") ? current.querySelector(".itemAction, button, a[href]") : null;
          (action || current).click();
        } else if ((e.key === "Backspace" || e.key === "BrowserBack" || e.key === "GoBack" || e.keyCode === 10009 || e.keyCode === 461) && !typing) {
          e.preventDefault();
          history.back();
        }
      },
      true
    );
    // A new page starts with nothing focused; the first arrow picks its first item.
    setInterval(function () {
      if (current && !document.contains(current)) current = null;
    }, 1000);
  }

  // Emby Web only (its index carries data-appversion); Jellyfin Web has its
  // own free TV layout.
  if (document.documentElement.hasAttribute("data-appversion") && tvWanted()) startTvMode();

  // Emby Web's router consumes hashchange, so watch the address instead.
  // Jellyfin Web leaves a dashboard route within milliseconds, faster than
  // the watcher below: check every hash change and history push at once.
  function onNavigate() {
    var r = route();
    if (r === lastRoute) return;
    lastRoute = r;
    checkAdminRoute();
  }
  window.addEventListener("hashchange", onNavigate, true);
  ["pushState", "replaceState"].forEach(function (name) {
    var orig = history[name];
    history[name] = function () {
      var out = orig.apply(this, arguments);
      onNavigate();
      return out;
    };
  });

  // The app signs in after this script starts: keep asking for the
  // settings until a signed-in client gets them.
  var lastRoute = null;
  var asking = false;
  setInterval(function () {
    if (!config && !asking && api()) {
      asking = true;
      loadConfig().then(function (c) {
        asking = false;
        if (c) armIdle();
      });
    }
    var r = route();
    if (r === lastRoute) return;
    lastRoute = r;
    checkAdminRoute();
  }, 500);
})();
