/* Stow launch surface — progressive enhancement only.
 *
 * The page is complete without this file. Everything here either adds
 * convenience (copy the install line) or freshness (a live star count that
 * would otherwise need a rebuild every time someone stars the repo).
 */

(function () {
  "use strict";

  // Copy buttons. The command is already selectable text, so this is a
  // convenience, never the only route to the value.
  document.querySelectorAll("[data-copy]").forEach(function (button) {
    var target = document.querySelector(button.getAttribute("data-copy"));
    if (!target) return;

    var label = button.textContent;
    var reset;

    button.addEventListener("click", function () {
      var text = target.textContent.trim();
      var done = function (ok) {
        button.textContent = ok ? "Copied" : "Press " + text.length + " keys";
        button.setAttribute("data-copied", ok ? "1" : "0");
        clearTimeout(reset);
        reset = setTimeout(function () {
          button.textContent = label;
          button.removeAttribute("data-copied");
        }, 1600);
      };

      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(
          function () {
            done(true);
          },
          function () {
            done(false);
          },
        );
        return;
      }

      // No clipboard permission: select it so the shortcut still works.
      var range = document.createRange();
      range.selectNodeContents(target);
      var selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
      done(false);
    });
  });

  // Live star count. Hidden until the fetch succeeds, so a failed request
  // leaves the plain "GitHub" link rather than a "0" that looks like a bug.
  var REPO = "chester-hill-solutions/stow-s3";

  fetch("https://api.github.com/repos/" + REPO, {
    headers: { Accept: "application/vnd.github+json" },
  })
    .then(function (response) {
      return response.ok ? response.json() : null;
    })
    .then(function (data) {
      if (!data || typeof data.stargazers_count !== "number") return;
      var stars = data.stargazers_count.toLocaleString("en");
      document.querySelectorAll("[data-stars]").forEach(function (node) {
        node.textContent = " " + stars;
        node.hidden = false;
      });
    })
    .catch(function () {
      /* the static markup is already correct without this */
    });
})();
