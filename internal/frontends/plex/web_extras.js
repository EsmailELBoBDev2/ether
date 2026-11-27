// Ether extras for the Plex Web app Ether serves. Ether is the server, so
// Plex Web's server settings and management pages open the matching page of
// the Ether console (same address, /ether) instead. Account and player
// settings stay in Plex Web. The app's code is untouched.
(function () {
  "use strict";
  var MAP = [
    [/\/(transcoder)(\b|$)/, "/playback"],
    [/\/(library|libraries|optimized|agents|extras)(\b|$)/, "/libraries"],
    [/\/(live-tv|livetv|dvr)/, "/livetv"],
    [/\/(scheduled-tasks|troubleshooting|console|logs)(\b|$)/, "/maintenance"],
    [/\/(plugins)(\b|$)/, "/plugins"],
    [/\/(network|remote|general|languages|dlna|web)(\b|$)/, "/server"],
  ];
  function route() {
    var h = location.hash || "";
    var i = h.indexOf("#!");
    return i < 0 ? "" : h.slice(i + 2).split("?")[0];
  }
  function check() {
    var r = route();
    var target = null;
    if (/^\/settings\/manage-library-access|^\/settings\/users/.test(r)) target = "/viewers";
    else if (/^\/settings\/server\//.test(r)) {
      target = "/server";
      for (var n = 0; n < MAP.length; n++) {
        if (MAP[n][0].test(r)) {
          target = MAP[n][1];
          break;
        }
      }
    }
    if (!target) return;
    window.open("/ether" + target, "_blank", "noopener");
    if (history.length > 1) history.back();
    else location.hash = "#!/";
  }
  window.addEventListener("hashchange", check);
  setTimeout(check, 1500);
})();
