(function () {
    var config = window.InviteCampaignAdminPage || {};
    var root = document.getElementById('invite-campaign-admin-app');
    if (!root) return;

    var STATUS_META = {
        0: { text: '进行中', className: 'status-ongoing' },
        1: { text: '已达标', className: 'status-completed' },
        2: { text: '已过期', className: 'status-expired' },
        3: { text: '已放弃', className: 'status-abandoned' },
        4: { text: '已使用', className: 'status-used' }
    };

    var state = {
        list: [],
        total: 0,
        page: 1,
        pageSize: 20,
        keyword: '',
        keywordType: 'email',
        status: '',
        detail: null,
        records: [],
        recordsTotal: 0,
        recordsPage: 1
    };

    function escapeHtml(value) {
        return String(value == null ? '' : value)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    function getToken() {
        return window.localStorage.getItem('authorization');
    }

    function getStatusMeta(status) {
        return STATUS_META.hasOwnProperty(status) ? STATUS_META[status] : {
            text: '未知',
            className: 'status-unknown'
        };
    }

    function redirectToLogin() {
        window.location.href = config.loginPath || '/';
    }

    async function api(path, options) {
        var requestOptions = options || {};
        var token = getToken();
        if (!token) {
            redirectToLogin();
            throw new Error('未登录');
        }

        requestOptions.headers = requestOptions.headers || {};
        requestOptions.headers.authorization = token;
        requestOptions.credentials = 'include';

        if (requestOptions.json) {
            requestOptions.method = requestOptions.method || 'POST';
            requestOptions.headers['Content-Type'] = 'application/json';
            requestOptions.body = JSON.stringify(requestOptions.json);
        } else {
            requestOptions.method = requestOptions.method || 'GET';
        }

        var response = await window.fetch((config.apiBase || '/api/v1') + path, requestOptions);
        var data = {};
        var contentType = response.headers.get('content-type') || '';
        if (contentType.indexOf('application/json') > -1) {
            data = await response.json();
        }
        if (response.status === 403) {
            redirectToLogin();
            throw new Error('登录已失效');
        }
        if (!response.ok) {
            throw new Error(data.message || data.msg || '请求失败');
        }
        return data;
    }

    function formatMoney(amount) {
        return '¥' + (Number(amount || 0) / 100).toFixed(2);
    }

    function formatDate(timestamp) {
        if (!timestamp) return '--';
        var date = new Date(Number(timestamp) * 1000);
        return [
            date.getFullYear(),
            String(date.getMonth() + 1).padStart(2, '0'),
            String(date.getDate()).padStart(2, '0')
        ].join('-') + ' ' + [
            String(date.getHours()).padStart(2, '0'),
            String(date.getMinutes()).padStart(2, '0'),
            String(date.getSeconds()).padStart(2, '0')
        ].join(':');
    }

    function formatCountdown(expiredAt) {
        if (!expiredAt) return '--';
        var remaining = Math.max(0, Number(expiredAt) - Math.floor(Date.now() / 1000));
        var days = Math.floor(remaining / 86400);
        var hours = Math.floor(remaining % 86400 / 3600);
        var minutes = Math.floor(remaining % 3600 / 60);
        var seconds = remaining % 60;
        return [days, hours, minutes, seconds].map(function (item) {
            return String(item).padStart(2, '0');
        }).join(':');
    }

    function toast(message, type) {
        var node = document.createElement('div');
        node.className = 'alert ' + (type === 'error' ? 'alert-danger' : 'alert-success');
        node.textContent = message;
        node.style.position = 'fixed';
        node.style.right = '20px';
        node.style.top = '20px';
        node.style.zIndex = '9999';
        node.style.minWidth = '220px';
        document.body.appendChild(node);
        window.setTimeout(function () {
            node.remove();
        }, 2200);
    }

    function renderLoading(message) {
        root.innerHTML = '<div class="campaign-card campaign-loading">' + escapeHtml(message || '加载中...') + '</div>';
    }

    function buildFilterQuery() {
        var params = new URLSearchParams();
        params.set('current', state.page);
        params.set('pageSize', state.pageSize);
        var index = 0;
        if (state.keyword) {
            params.set('filter[' + index + '][key]', state.keywordType);
            params.set('filter[' + index + '][condition]', state.keywordType === 'email' ? '=' : '模糊');
            params.set('filter[' + index + '][value]', state.keyword);
            index += 1;
        }
        if (state.status !== '') {
            params.set('filter[' + index + '][key]', 'status');
            params.set('filter[' + index + '][condition]', '=');
            params.set('filter[' + index + '][value]', state.status);
        }
        return params.toString();
    }

    function renderList() {
        var totalPages = Math.max(1, Math.ceil(state.total / state.pageSize));
        var activeCount = state.list.filter(function (item) { return Number(item.status) === 0; }).length;
        var completedCount = state.list.filter(function (item) { return Number(item.status) === 1; }).length;
        var usedCount = state.list.filter(function (item) { return Number(item.status) === 4; }).length;
        var expiredCount = state.list.filter(function (item) { return Number(item.status) === 2; }).length;

        var rows = state.list.length ? state.list.map(function (item) {
            var status = getStatusMeta(Number(item.status));
            var progress = Number(item.target_amount) > 0
                ? Math.min(100, Math.round(Number(item.current_amount) / Number(item.target_amount) * 100))
                : 0;
            return '' +
                '<tr>' +
                    '<td>#' + escapeHtml(item.id) + '</td>' +
                    '<td>' + escapeHtml(item.user_email || '--') + '</td>' +
                    '<td>' + escapeHtml(item.plan_name || '--') + '<div class="campaign-subvalue">' + escapeHtml(item.period || '--') + '</div></td>' +
                    '<td>' + escapeHtml(item.invite_code || '--') + '</td>' +
                    '<td><span class="status-badge ' + status.className + '">' + status.text + '</span></td>' +
                    '<td>' +
                        '<div style="min-width:180px;">' +
                            '<div class="campaign-progress-bar"><div class="campaign-progress-fill" style="width:' + progress + '%;"></div></div>' +
                            '<div class="campaign-progress-text">' + escapeHtml(formatMoney(item.current_amount)) + ' / ' + escapeHtml(formatMoney(item.target_amount)) + '</div>' +
                        '</div>' +
                    '</td>' +
                    '<td>' + (Number(item.status) === 0 ? escapeHtml(formatCountdown(item.expired_at)) : '--') + '</td>' +
                    '<td>' + escapeHtml(item.used_order_trade_no || '--') + '</td>' +
                    '<td>' + escapeHtml(formatDate(item.created_at)) + '</td>' +
                    '<td><button class="btn btn-sm btn-alt-primary admin-campaign-detail-btn" data-id="' + escapeHtml(item.id) + '">详情</button></td>' +
                '</tr>';
        }).join('') : '<tr><td colspan="10" class="campaign-empty">暂无任务</td></tr>';

        root.innerHTML = '' +
            '<div class="campaign-shell">' +
                '<div class="campaign-hero">' +
                    '<div>' +
                        '<h1>任务管理</h1>' +
                        '<p>查看邀请减免活动任务、绑定邀请码、完成进度和触发的实际抵扣订单。</p>' +
                    '</div>' +
                    '<div class="campaign-actions">' +
                        '<a class="btn btn-alt-secondary" href="' + escapeHtml(config.backPath || '/') + '">返回后台</a>' +
                        '<button class="btn btn-primary" id="admin-campaign-refresh">刷新列表</button>' +
                    '</div>' +
                '</div>' +
                '<div class="campaign-quick-stats">' +
                    '<div class="campaign-stat"><span class="campaign-label">总任务数</span><div class="campaign-value">' + escapeHtml(state.total) + '</div></div>' +
                    '<div class="campaign-stat"><span class="campaign-label">当前页进行中</span><div class="campaign-value">' + escapeHtml(activeCount) + '</div></div>' +
                    '<div class="campaign-stat"><span class="campaign-label">当前页已达标</span><div class="campaign-value">' + escapeHtml(completedCount) + '</div></div>' +
                    '<div class="campaign-stat"><span class="campaign-label">当前页已使用/过期</span><div class="campaign-value">' + escapeHtml(usedCount + expiredCount) + '</div></div>' +
                '</div>' +
                '<div class="campaign-card">' +
                    '<div class="campaign-toolbar">' +
                        '<div class="campaign-field">' +
                            '<label>搜索字段</label>' +
                            '<select id="admin-campaign-keyword-type">' +
                                '<option value="email"' + (state.keywordType === 'email' ? ' selected' : '') + '>邀请人邮箱</option>' +
                                '<option value="invite_code"' + (state.keywordType === 'invite_code' ? ' selected' : '') + '>邀请码</option>' +
                            '</select>' +
                        '</div>' +
                        '<div class="campaign-field">' +
                            '<label>关键词</label>' +
                            '<input id="admin-campaign-keyword" value="' + escapeHtml(state.keyword) + '" placeholder="邮箱或邀请码">' +
                        '</div>' +
                        '<div class="campaign-field">' +
                            '<label>状态</label>' +
                            '<select id="admin-campaign-status">' +
                                '<option value="">全部状态</option>' +
                                '<option value="0"' + (String(state.status) === '0' ? ' selected' : '') + '>进行中</option>' +
                                '<option value="1"' + (String(state.status) === '1' ? ' selected' : '') + '>已达标</option>' +
                                '<option value="2"' + (String(state.status) === '2' ? ' selected' : '') + '>已过期</option>' +
                                '<option value="3"' + (String(state.status) === '3' ? ' selected' : '') + '>已放弃</option>' +
                                '<option value="4"' + (String(state.status) === '4' ? ' selected' : '') + '>已使用</option>' +
                            '</select>' +
                        '</div>' +
                        '<button class="btn btn-primary" id="admin-campaign-search">搜索</button>' +
                    '</div>' +
                    '<table class="campaign-table">' +
                        '<thead><tr><th>ID</th><th>邀请人</th><th>目标套餐</th><th>邀请码</th><th>状态</th><th>进度</th><th>倒计时</th><th>抵扣订单</th><th>创建时间</th><th>操作</th></tr></thead>' +
                        '<tbody>' + rows + '</tbody>' +
                    '</table>' +
                    '<div class="campaign-pagination">' +
                        '<button class="btn btn-sm btn-alt-secondary" id="admin-campaign-prev"' + (state.page <= 1 ? ' disabled' : '') + '>上一页</button>' +
                        '<span>第 ' + state.page + ' / ' + totalPages + ' 页</span>' +
                        '<button class="btn btn-sm btn-alt-secondary" id="admin-campaign-next"' + (state.page >= totalPages ? ' disabled' : '') + '>下一页</button>' +
                    '</div>' +
                '</div>' +
                '<div id="admin-campaign-detail-wrap"></div>' +
            '</div>';

        bindListEvents();
        renderDetail();
    }

    function renderDetail() {
        var mount = document.getElementById('admin-campaign-detail-wrap');
        if (!mount) return;
        if (!state.detail) {
            mount.innerHTML = '';
            return;
        }

        var detail = state.detail;
        var status = getStatusMeta(Number(detail.status));
        var progress = Number(detail.target_amount) > 0
            ? Math.min(100, Math.round(Number(detail.current_amount) / Number(detail.target_amount) * 100))
            : 0;
        var totalPages = Math.max(1, Math.ceil(state.recordsTotal / 10));
        var rows = state.records.length ? state.records.map(function (item) {
            return '<tr>' +
                '<td>' + escapeHtml(formatDate(item.created_at)) + '</td>' +
                '<td>' + escapeHtml(item.invitee_email || ('#' + item.invitee_user_id)) + '</td>' +
                '<td>' + escapeHtml(item.invite_code || '--') + '</td>' +
                '<td>' + escapeHtml(formatMoney(item.reward_amount || 0)) + '</td>' +
                '</tr>';
        }).join('') : '<tr><td colspan="4" class="campaign-empty">暂无注册记录</td></tr>';

        mount.innerHTML = '' +
            '<div class="campaign-split" style="margin-top:20px;">' +
                '<div class="campaign-card">' +
                    '<div class="campaign-hero" style="margin-bottom:16px;">' +
                        '<div><h3>任务详情 #' + escapeHtml(detail.id) + '</h3><p>绑定邀请码：' + escapeHtml(detail.invite_code || '--') + '</p></div>' +
                        '<span class="status-badge ' + status.className + '">' + status.text + '</span>' +
                    '</div>' +
                    '<div class="campaign-progress">' +
                        '<div class="campaign-progress-bar"><div class="campaign-progress-fill" style="width:' + progress + '%;"></div></div>' +
                        '<div class="campaign-progress-text">' + escapeHtml(formatMoney(detail.current_amount)) + ' / ' + escapeHtml(formatMoney(detail.target_amount)) + '</div>' +
                    '</div>' +
                    '<div style="margin-top:16px;">' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">邀请人邮箱</div><div class="campaign-kv-value">' + escapeHtml(detail.user && detail.user.email || detail.user_email || '--') + '</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">目标套餐</div><div class="campaign-kv-value">' + escapeHtml(detail.plan && detail.plan.name || '--') + ' / ' + escapeHtml(detail.period || '--') + '</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">单次奖励</div><div class="campaign-kv-value">' + escapeHtml(formatMoney(detail.reward_amount)) + '</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">邀请人数</div><div class="campaign-kv-value">' + escapeHtml(detail.invite_count) + '</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">任务时效</div><div class="campaign-kv-value">' + escapeHtml(formatDate(detail.started_at)) + ' 至 ' + escapeHtml(formatDate(detail.expired_at)) + '</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">绑定邀请码 ID</div><div class="campaign-kv-value">' + escapeHtml(detail.invite_code_id || '--') + '</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">使用订单</div><div class="campaign-kv-value">' + escapeHtml(detail.used_order && detail.used_order.trade_no || '--') + '</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">订单抵扣</div><div class="campaign-kv-value">' + escapeHtml(detail.used_order ? formatMoney(detail.used_order.invite_campaign_discount_amount || 0) : '--') + '</div></div>' +
                    '</div>' +
                '</div>' +
                '<div class="campaign-card">' +
                    '<h3>注册记录</h3>' +
                    '<table class="campaign-table">' +
                        '<thead><tr><th>注册时间</th><th>被邀请用户</th><th>邀请码</th><th>奖励</th></tr></thead>' +
                        '<tbody>' + rows + '</tbody>' +
                    '</table>' +
                    '<div class="campaign-pagination">' +
                        '<button class="btn btn-sm btn-alt-secondary" id="admin-record-prev"' + (state.recordsPage <= 1 ? ' disabled' : '') + '>上一页</button>' +
                        '<span>第 ' + state.recordsPage + ' / ' + totalPages + ' 页</span>' +
                        '<button class="btn btn-sm btn-alt-secondary" id="admin-record-next"' + (state.recordsPage >= totalPages ? ' disabled' : '') + '>下一页</button>' +
                    '</div>' +
                '</div>' +
            '</div>';

        bindDetailEvents();
    }

    function bindListEvents() {
        var search = document.getElementById('admin-campaign-search');
        var refresh = document.getElementById('admin-campaign-refresh');
        var prev = document.getElementById('admin-campaign-prev');
        var next = document.getElementById('admin-campaign-next');
        var type = document.getElementById('admin-campaign-keyword-type');
        var keyword = document.getElementById('admin-campaign-keyword');
        var status = document.getElementById('admin-campaign-status');
        var detailButtons = document.querySelectorAll('.admin-campaign-detail-btn');

        if (search) {
            search.onclick = function () {
                state.page = 1;
                state.keywordType = type.value;
                state.keyword = keyword.value.trim();
                state.status = status.value;
                loadList();
            };
        }
        if (refresh) {
            refresh.onclick = function () {
                loadList();
            };
        }
        if (prev) {
            prev.onclick = function () {
                if (state.page > 1) {
                    state.page -= 1;
                    loadList();
                }
            };
        }
        if (next) {
            next.onclick = function () {
                var totalPages = Math.max(1, Math.ceil(state.total / state.pageSize));
                if (state.page < totalPages) {
                    state.page += 1;
                    loadList();
                }
            };
        }
        detailButtons.forEach(function (button) {
            button.onclick = function () {
                loadDetail(button.getAttribute('data-id'));
            };
        });
    }

    function bindDetailEvents() {
        var prev = document.getElementById('admin-record-prev');
        var next = document.getElementById('admin-record-next');
        if (prev) {
            prev.onclick = function () {
                if (state.recordsPage > 1) {
                    loadRecords(state.recordsPage - 1);
                }
            };
        }
        if (next) {
            next.onclick = function () {
                var totalPages = Math.max(1, Math.ceil(state.recordsTotal / 10));
                if (state.recordsPage < totalPages) {
                    loadRecords(state.recordsPage + 1);
                }
            };
        }
    }

    async function loadList() {
        renderLoading('正在加载任务列表...');
        try {
            var response = await api('/' + config.securePath + '/invite/campaign/fetch?' + buildFilterQuery());
            state.list = response.data || [];
            state.total = response.total || 0;
            renderList();
        } catch (error) {
            root.innerHTML = '<div class="campaign-card campaign-empty">' + escapeHtml(error.message || '加载失败') + '</div>';
        }
    }

    async function loadDetail(id) {
        try {
            var detailResponse = await api('/' + config.securePath + '/invite/campaign/detail', {
                method: 'POST',
                json: {
                    id: Number(id)
                }
            });
            state.detail = detailResponse.data || null;
            state.recordsPage = 1;
            await loadRecords(1);
            renderList();
        } catch (error) {
            toast(error.message || '加载详情失败', 'error');
        }
    }

    async function loadRecords(page) {
        if (!state.detail) return;
        state.recordsPage = page || 1;
        var response = await api('/' + config.securePath + '/invite/campaign/records?campaign_id=' + state.detail.id + '&current=' + state.recordsPage + '&page_size=10');
        state.records = response.data || [];
        state.recordsTotal = response.total || 0;
    }

    loadList();
})();
