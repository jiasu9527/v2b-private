(function () {
    function buildSidebarLink() {
        var nav = document.querySelector('#sidebar .nav-main');
        if (!nav || nav.querySelector('.js-invite-campaign-nav')) return;

        var linkItem = document.createElement('li');
        linkItem.className = 'nav-main-item js-invite-campaign-nav';
        linkItem.innerHTML = '' +
            '<a class="nav-main-link" href="/invite-campaign">' +
                '<i class="nav-main-link-icon si si-present"></i>' +
                '<span class="nav-main-link-name">邀请活动</span>' +
            '</a>';

        var anchors = Array.prototype.slice.call(nav.querySelectorAll('.nav-main-link-name'));
        var inviteAnchor = null;
        anchors.forEach(function (node) {
            if (node.textContent && node.textContent.indexOf('我的邀请') > -1) {
                inviteAnchor = node;
            }
        });

        var insertAfter = inviteAnchor ? inviteAnchor.closest('.nav-main-item') : null;
        if (insertAfter && insertAfter.parentNode) {
            insertAfter.parentNode.insertBefore(linkItem, insertAfter.nextSibling);
            return;
        }
        nav.appendChild(linkItem);
    }

    function buildInvitePageButton() {
        if (window.location.hash.indexOf('#/invite') !== 0) return;
        if (document.querySelector('.js-open-invite-campaign-page')) return;

        var headers = Array.prototype.slice.call(document.querySelectorAll('.block-header'));
        var targetHeader = null;
        headers.forEach(function (header) {
            var title = header.querySelector('.block-title');
            if (title && title.textContent && title.textContent.indexOf('邀请码管理') > -1) {
                targetHeader = header;
            }
        });

        if (!targetHeader) return;

        var options = targetHeader.querySelector('.block-options');
        if (!options) {
            options = document.createElement('div');
            options.className = 'block-options';
            targetHeader.appendChild(options);
        }

        var button = document.createElement('a');
        button.className = 'btn btn-sm btn-alt-primary js-open-invite-campaign-page';
        button.href = '/invite-campaign';
        button.textContent = '邀请活动';
        options.insertBefore(button, options.firstChild);
    }

    function run() {
        buildSidebarLink();
        buildInvitePageButton();
    }

    var observer = new MutationObserver(function () {
        run();
    });

    window.addEventListener('hashchange', run);
    window.addEventListener('load', run);

    observer.observe(document.documentElement, {
        childList: true,
        subtree: true
    });
})();
