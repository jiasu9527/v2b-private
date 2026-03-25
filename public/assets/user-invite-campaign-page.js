(function () {
    var config = window.InviteCampaignUserPage || {};
    var root = document.getElementById('invite-campaign-user-app');
    if (!root) return;

    var PERIODS = [
        { key: 'month_price', label: '月付' },
        { key: 'quarter_price', label: '季付' },
        { key: 'half_year_price', label: '半年付' },
        { key: 'year_price', label: '年付' },
        { key: 'two_year_price', label: '两年付' },
        { key: 'three_year_price', label: '三年付' },
        { key: 'onetime_price', label: '一次性' }
    ];

    var STATUS_META = {
        0: { text: '进行中', className: 'status-ongoing' },
        1: { text: '已达标', className: 'status-completed' },
        2: { text: '已过期', className: 'status-expired' },
        3: { text: '已放弃', className: 'status-abandoned' },
        4: { text: '已使用', className: 'status-used' }
    };

    var state = {
        campaign: null,
        records: [],
        recordsPage: 1,
        recordsTotal: 0,
        plans: [],
        selectedPlanId: null,
        selectedPeriod: '',
        currency: 'CNY',
        currencySymbol: '¥',
        countdownTimer: null
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

    function redirectToLogin() {
        window.location.href = config.loginPath || '/#/login';
    }

    async function api(path, options) {
        var requestOptions = options || {};
        var headers = requestOptions.headers || {};
        var token = getToken();
        if (!token) {
            redirectToLogin();
            throw new Error('未登录');
        }
        headers.authorization = token;
        if (requestOptions.json) {
            headers['Content-Type'] = 'application/json';
            requestOptions.body = JSON.stringify(requestOptions.json);
        }
        requestOptions.headers = headers;
        requestOptions.credentials = 'include';
        requestOptions.method = requestOptions.method || 'GET';

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
        if (data.code && data.code !== 200) {
            throw new Error(data.message || data.msg || '请求失败');
        }
        return data;
    }

    function formatMoney(amount) {
        var value = Number(amount || 0) / 100;
        try {
            return new Intl.NumberFormat('zh-CN', {
                style: 'currency',
                currency: state.currency || 'CNY',
                minimumFractionDigits: 2
            }).format(value);
        } catch (error) {
            return (state.currencySymbol || '¥') + value.toFixed(2);
        }
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
        var remaining = Math.max(0, Number(expiredAt || 0) - Math.floor(Date.now() / 1000));
        var days = Math.floor(remaining / 86400);
        var hours = Math.floor(remaining % 86400 / 3600);
        var minutes = Math.floor(remaining % 3600 / 60);
        var seconds = remaining % 60;
        return [days, hours, minutes, seconds].map(function (item) {
            return String(item).padStart(2, '0');
        }).join(':');
    }

    function getStatusMeta(status) {
        return STATUS_META.hasOwnProperty(status) ? STATUS_META[status] : {
            text: '未知',
            className: 'status-unknown'
        };
    }

    function getPlanById(planId) {
        var matched = null;
        state.plans.forEach(function (plan) {
            if (String(plan.id) === String(planId)) {
                matched = plan;
            }
        });
        return matched;
    }

    function getPlanPeriods(plan) {
        return PERIODS.filter(function (item) {
            return plan && plan[item.key] !== null && plan[item.key] !== undefined;
        });
    }

    function currentInviteLink() {
        if (!state.campaign || !state.campaign.invite_code) return '';
        var origin = window.location.origin.replace(/\/$/, '');
        return origin + '/#/register?code=' + encodeURIComponent(state.campaign.invite_code);
    }

    function showToast(message, type) {
        var toast = document.createElement('div');
        toast.className = 'alert ' + (type === 'error' ? 'alert-danger' : 'alert-success');
        toast.textContent = message;
        toast.style.position = 'fixed';
        toast.style.right = '20px';
        toast.style.top = '20px';
        toast.style.zIndex = '9999';
        toast.style.minWidth = '220px';
        document.body.appendChild(toast);
        window.setTimeout(function () {
            toast.remove();
        }, 2200);
    }

    async function copyText(text) {
        if (!text) {
            showToast('没有可复制的内容', 'error');
            return;
        }
        try {
            await navigator.clipboard.writeText(text);
            showToast('复制成功');
        } catch (error) {
            var input = document.createElement('textarea');
            input.value = text;
            document.body.appendChild(input);
            input.select();
            document.execCommand('copy');
            input.remove();
            showToast('复制成功');
        }
    }

    function renderLoading(message) {
        root.innerHTML = '<div class="campaign-card campaign-loading">' + escapeHtml(message || '加载中...') + '</div>';
    }

    function renderError(message) {
        root.innerHTML = '<div class="campaign-card campaign-empty">' + escapeHtml(message || '加载失败') + '</div>';
    }

    function bindRecordsPager() {
        var prev = document.getElementById('user-campaign-records-prev');
        var next = document.getElementById('user-campaign-records-next');
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

    function bindCampaignActions() {
        var copyCode = document.getElementById('user-campaign-copy-code');
        var copyLink = document.getElementById('user-campaign-copy-link');
        var pay = document.getElementById('user-campaign-pay');
        var abandon = document.getElementById('user-campaign-abandon');

        if (copyCode) {
            copyCode.onclick = function () {
                copyText(state.campaign && state.campaign.invite_code);
            };
        }
        if (copyLink) {
            copyLink.onclick = function () {
                copyText(currentInviteLink());
            };
        }
        if (pay) {
            pay.onclick = async function () {
                pay.disabled = true;
                pay.textContent = '正在创建订单...';
                try {
                    var response = await api('/user/order/save', {
                        method: 'POST',
                        json: {
                            plan_id: state.campaign.plan_id,
                            period: state.campaign.period
                        }
                    });
                    var tradeNo = response.data;
                    window.location.href = (config.orderPathPrefix || '/#/order/') + encodeURIComponent(tradeNo);
                } catch (error) {
                    pay.disabled = false;
                    pay.textContent = '立即下单';
                    showToast(error.message || '创建订单失败', 'error');
                }
            };
        }
        if (abandon) {
            abandon.onclick = async function () {
                if (!window.confirm('确认放弃当前邀请活动任务吗？')) return;
                abandon.disabled = true;
                try {
                    await api('/user/invite/campaign/abandon', {
                        method: 'POST',
                        json: {}
                    });
                    showToast('任务已放弃');
                    await loadCampaign();
                } catch (error) {
                    abandon.disabled = false;
                    showToast(error.message || '放弃失败', 'error');
                }
            };
        }
    }

    function renderCampaign() {
        var campaign = state.campaign;
        var status = getStatusMeta(campaign.status);
        var progress = campaign.target_amount > 0
            ? Math.min(100, Math.round(campaign.current_amount / campaign.target_amount * 100))
            : 0;
        var totalPages = Math.max(1, Math.ceil(state.recordsTotal / 10));
        var rows = state.records.length ? state.records.map(function (record) {
            return '<tr>' +
                '<td>' + escapeHtml(formatDate(record.created_at)) + '</td>' +
                '<td>' + escapeHtml(record.invitee_email || ('#' + record.invitee_user_id)) + '</td>' +
                '<td>' + escapeHtml(record.invite_code || '--') + '</td>' +
                '<td>' + escapeHtml(formatMoney(record.reward_amount || 0)) + '</td>' +
                '</tr>';
        }).join('') : '<tr><td colspan="4" class="campaign-empty">暂无任务记录</td></tr>';

        root.innerHTML = '' +
            '<div class="campaign-grid">' +
                '<div class="campaign-main">' +
                    '<div class="campaign-card">' +
                        '<div class="campaign-hero">' +
                            '<div>' +
                                '<h1>邀请活动任务</h1>' +
                                '<p>邀请好友注册后，每注册 1 人累计抵扣 10 元。任务有效期 48 小时，达标后购买绑定套餐会自动抵扣。</p>' +
                            '</div>' +
                            '<span class="status-badge ' + status.className + '">' + status.text + '</span>' +
                        '</div>' +
                        '<div class="campaign-meta">' +
                            '<div class="campaign-meta-item">' +
                                '<span class="campaign-label">目标套餐</span>' +
                                '<div class="campaign-value">' + escapeHtml((campaign.plan && campaign.plan.name) || '--') + '</div>' +
                                '<div class="campaign-subvalue">' + escapeHtml((PERIODS.find(function (item) { return item.key === campaign.period; }) || { label: campaign.period }).label) + '</div>' +
                            '</div>' +
                            '<div class="campaign-meta-item">' +
                                '<span class="campaign-label">倒计时</span>' +
                                '<div class="campaign-value" id="user-campaign-countdown">' + (campaign.status === 0 ? escapeHtml(formatCountdown(campaign.expired_at)) : '--') + '</div>' +
                                '<div class="campaign-subvalue">截止于 ' + escapeHtml(formatDate(campaign.expired_at)) + '</div>' +
                            '</div>' +
                        '</div>' +
                        '<div class="campaign-stats" style="margin-top:14px;">' +
                            '<div class="campaign-stat"><span class="campaign-label">目标金额</span><div class="campaign-value">' + escapeHtml(formatMoney(campaign.target_amount)) + '</div></div>' +
                            '<div class="campaign-stat"><span class="campaign-label">当前减免</span><div class="campaign-value success">' + escapeHtml(formatMoney(campaign.current_amount)) + '</div></div>' +
                            '<div class="campaign-stat"><span class="campaign-label">还差金额</span><div class="campaign-value">' + escapeHtml(formatMoney(campaign.remaining_amount)) + '</div></div>' +
                            '<div class="campaign-stat"><span class="campaign-label">单次奖励</span><div class="campaign-value">' + escapeHtml(formatMoney(campaign.reward_amount)) + '</div></div>' +
                            '<div class="campaign-stat"><span class="campaign-label">已邀人数</span><div class="campaign-value">' + escapeHtml(campaign.invite_count) + '</div></div>' +
                            '<div class="campaign-stat"><span class="campaign-label">可用抵扣</span><div class="campaign-value success">' + escapeHtml(formatMoney(campaign.discount_amount)) + '</div></div>' +
                        '</div>' +
                        '<div class="campaign-progress">' +
                            '<div class="campaign-progress-bar"><div class="campaign-progress-fill" style="width:' + progress + '%;"></div></div>' +
                            '<div class="campaign-progress-text">当前进度 ' + progress + '%</div>' +
                        '</div>' +
                        '<div class="campaign-link-row" style="margin-top:18px;">' +
                            '<div class="campaign-field">' +
                                '<label>邀请码</label>' +
                                '<input type="text" readonly value="' + escapeHtml(campaign.invite_code || '') + '">' +
                            '</div>' +
                            '<button id="user-campaign-copy-code" class="btn btn-alt-primary">复制邀请码</button>' +
                        '</div>' +
                        '<div class="campaign-link-row" style="margin-top:12px;">' +
                            '<div class="campaign-field">' +
                                '<label>邀请链接</label>' +
                                '<input type="text" readonly value="' + escapeHtml(currentInviteLink()) + '">' +
                            '</div>' +
                            '<button id="user-campaign-copy-link" class="btn btn-primary">复制链接</button>' +
                        '</div>' +
                        '<div class="campaign-note" style="margin-top:18px;">只有活动绑定的套餐和周期下单时才会使用这次抵扣。当前任务会绑定到你原有长期邀请码，被邀请人注册后自动累计进度。</div>' +
                        '<div class="campaign-actions" style="margin-top:18px;">' +
                            '<button id="user-campaign-pay" class="btn btn-primary"' + (campaign.status === 2 || campaign.status === 3 || campaign.status === 4 ? ' disabled' : '') + '>立即下单</button>' +
                            '<a class="btn btn-alt-secondary" href="' + escapeHtml(config.planPath || '/#/plan') + '">查看套餐</a>' +
                            '<button id="user-campaign-abandon" class="btn btn-alt-danger"' + (campaign.status !== 0 ? ' disabled' : '') + '>放弃任务</button>' +
                        '</div>' +
                    '</div>' +
                    '<div class="campaign-card">' +
                        '<h3>邀请记录</h3>' +
                        '<table class="campaign-table">' +
                            '<thead><tr><th>注册时间</th><th>邀请用户</th><th>使用邀请码</th><th>累计奖励</th></tr></thead>' +
                            '<tbody>' + rows + '</tbody>' +
                        '</table>' +
                        '<div class="campaign-pagination">' +
                            '<button id="user-campaign-records-prev" class="btn btn-sm btn-alt-secondary"' + (state.recordsPage <= 1 ? ' disabled' : '') + '>上一页</button>' +
                            '<span>第 ' + state.recordsPage + ' / ' + totalPages + ' 页</span>' +
                            '<button id="user-campaign-records-next" class="btn btn-sm btn-alt-secondary"' + (state.recordsPage >= totalPages ? ' disabled' : '') + '>下一页</button>' +
                        '</div>' +
                    '</div>' +
                '</div>' +
                '<div class="campaign-side">' +
                    '<div class="campaign-card">' +
                        '<h3>任务说明</h3>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">任务模式</div><div class="campaign-kv-value">单套餐活动</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">奖励规则</div><div class="campaign-kv-value">每邀请 1 人注册，增加 10 元套餐抵扣</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">任务期限</div><div class="campaign-kv-value">创建后固定 48 小时</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">试用规则</div><div class="campaign-kv-value">被邀请人沿用现有试用套餐逻辑</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">邀请码</div><div class="campaign-kv-value">绑定到你当前长期邀请码</div></div>' +
                    '</div>' +
                    '<div class="campaign-card">' +
                        '<h3>快捷入口</h3>' +
                        '<div class="campaign-actions">' +
                            '<a class="btn btn-alt-secondary" href="' + escapeHtml(config.invitePath || '/#/invite') + '">返回邀请页</a>' +
                            '<a class="btn btn-alt-secondary" href="' + escapeHtml(config.dashboardPath || '/#/dashboard') + '">返回仪表盘</a>' +
                        '</div>' +
                    '</div>' +
                '</div>' +
            '</div>';

        bindCampaignActions();
        bindRecordsPager();
        startCountdown();
    }

    function bindCreateForm() {
        var planSelect = document.getElementById('user-campaign-plan');
        var periodSelect = document.getElementById('user-campaign-period');
        var createButton = document.getElementById('user-campaign-create');
        if (planSelect) {
            planSelect.onchange = function () {
                state.selectedPlanId = planSelect.value;
                state.selectedPeriod = '';
                renderCreateForm();
            };
        }
        if (periodSelect) {
            periodSelect.onchange = function () {
                state.selectedPeriod = periodSelect.value;
                renderCreateForm();
            };
        }
        if (createButton) {
            createButton.onclick = async function () {
                if (!state.selectedPlanId || !state.selectedPeriod) {
                    showToast('请先选择套餐和周期', 'error');
                    return;
                }
                createButton.disabled = true;
                createButton.textContent = '正在创建...';
                try {
                    await api('/user/invite/campaign/save', {
                        method: 'POST',
                        json: {
                            plan_id: Number(state.selectedPlanId),
                            period: state.selectedPeriod
                        }
                    });
                    showToast('活动任务已创建');
                    await loadCampaign();
                } catch (error) {
                    createButton.disabled = false;
                    createButton.textContent = '开始任务';
                    showToast(error.message || '创建失败', 'error');
                }
            };
        }
    }

    function renderCreateForm() {
        var plan = getPlanById(state.selectedPlanId) || state.plans[0] || null;
        if (plan && !state.selectedPlanId) {
            state.selectedPlanId = String(plan.id);
        }
        var periods = getPlanPeriods(plan);
        if (!state.selectedPeriod && periods.length) {
            state.selectedPeriod = periods[0].key;
        }
        var selectedPeriodMeta = PERIODS.find(function (item) {
            return item.key === state.selectedPeriod;
        });
        var targetAmount = plan && state.selectedPeriod ? plan[state.selectedPeriod] : 0;

        root.innerHTML = '' +
            '<div class="campaign-grid">' +
                '<div class="campaign-main">' +
                    '<div class="campaign-card">' +
                        '<div class="campaign-hero">' +
                            '<div>' +
                                '<h1>邀请活动任务</h1>' +
                                '<p>你当前还没有进行中的任务。直接在这里选择目标套餐和周期，就能立即生成一个新的邀请减免活动。</p>' +
                            '</div>' +
                            '<span class="status-badge status-unknown">待创建</span>' +
                        '</div>' +
                        '<div class="campaign-form-grid">' +
                            '<div class="campaign-field">' +
                                '<label>目标套餐</label>' +
                                '<select id="user-campaign-plan">' +
                                    state.plans.map(function (item) {
                                        return '<option value="' + escapeHtml(item.id) + '"' + (String(item.id) === String(state.selectedPlanId) ? ' selected' : '') + '>' + escapeHtml(item.name) + '</option>';
                                    }).join('') +
                                '</select>' +
                            '</div>' +
                            '<div class="campaign-field">' +
                                '<label>购买周期</label>' +
                                '<select id="user-campaign-period">' +
                                    periods.map(function (item) {
                                        return '<option value="' + escapeHtml(item.key) + '"' + (item.key === state.selectedPeriod ? ' selected' : '') + '>' + escapeHtml(item.label) + ' · ' + escapeHtml(formatMoney(plan[item.key])) + '</option>';
                                    }).join('') +
                                '</select>' +
                            '</div>' +
                        '</div>' +
                        '<div class="campaign-stats" style="margin-top:14px;">' +
                            '<div class="campaign-stat"><span class="campaign-label">目标金额</span><div class="campaign-value">' + escapeHtml(formatMoney(targetAmount)) + '</div></div>' +
                            '<div class="campaign-stat"><span class="campaign-label">单次奖励</span><div class="campaign-value">' + escapeHtml(formatMoney(1000)) + '</div></div>' +
                            '<div class="campaign-stat"><span class="campaign-label">任务有效期</span><div class="campaign-value">48 小时</div></div>' +
                        '</div>' +
                        '<div class="campaign-note" style="margin-top:18px;">创建后会自动绑定到你的长期邀请码。被邀请人先建立普通邀请关系，再判断当前是否有有效任务；有任务则累加减免，无任务则只走普通返佣。</div>' +
                        '<div class="campaign-actions" style="margin-top:18px;">' +
                            '<button id="user-campaign-create" class="btn btn-primary">开始任务</button>' +
                            '<a class="btn btn-alt-secondary" href="' + escapeHtml(config.planPath || '/#/plan') + '">先看套餐</a>' +
                            '<a class="btn btn-alt-secondary" href="' + escapeHtml(config.invitePath || '/#/invite') + '">返回邀请页</a>' +
                        '</div>' +
                    '</div>' +
                '</div>' +
                '<div class="campaign-side">' +
                    '<div class="campaign-card">' +
                        '<h3>创建须知</h3>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">任务类型</div><div class="campaign-kv-value">单套餐活动（推荐）</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">试用奖励</div><div class="campaign-kv-value">复用现有试用套餐</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">抵扣增加</div><div class="campaign-kv-value">每邀请 1 人增加 10 元</div></div>' +
                        '<div class="campaign-kv"><div class="campaign-kv-key">触发时机</div><div class="campaign-kv-value">被邀请人注册成功后自动累计</div></div>' +
                    '</div>' +
                '</div>' +
            '</div>';

        bindCreateForm();
    }

    function startCountdown() {
        if (state.countdownTimer) {
            window.clearInterval(state.countdownTimer);
            state.countdownTimer = null;
        }
        if (!state.campaign || state.campaign.status !== 0) return;
        var countdown = document.getElementById('user-campaign-countdown');
        if (!countdown) return;
        state.countdownTimer = window.setInterval(function () {
            if (!state.campaign) return;
            countdown.textContent = formatCountdown(state.campaign.expired_at);
        }, 1000);
    }

    async function loadRecords(page) {
        if (!state.campaign) return;
        state.recordsPage = page || 1;
        var response = await api('/user/invite/campaign/records?current=' + state.recordsPage + '&page_size=10');
        state.records = response.data || [];
        state.recordsTotal = response.total || 0;
        renderCampaign();
    }

    async function loadCampaign() {
        renderLoading('正在加载活动任务...');
        try {
            var responses = await Promise.all([
                api('/user/invite/campaign/fetch'),
                api('/user/comm/config')
            ]);
            state.campaign = responses[0].data || null;
            state.currency = responses[1].data && responses[1].data.currency || 'CNY';
            state.currencySymbol = responses[1].data && responses[1].data.currency_symbol || '¥';

            if (state.campaign) {
                await loadRecords(1);
                return;
            }

            var planResponse = await api('/user/plan/fetch');
            state.plans = (planResponse.data || []).filter(function (plan) {
                return getPlanPeriods(plan).length > 0;
            });
            if (!state.plans.length) {
                renderError('当前没有可参与活动的套餐');
                return;
            }
            state.selectedPlanId = String(state.plans[0].id);
            state.selectedPeriod = getPlanPeriods(state.plans[0])[0].key;
            renderCreateForm();
        } catch (error) {
            renderError(error.message || '加载失败');
        }
    }

    loadCampaign();
})();
