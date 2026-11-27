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
          window.open(c.console.replace(/\/$/, "") + target.replace("APP", c.app || "emby"), "_blank", "noopener");
          if (history.length > 1) history.back();
          else location.hash = c.app === "jellyfin" ? "#/home" : "#!/home";
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

  // Emby Web's router consumes hashchange, so watch the address instead.
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
