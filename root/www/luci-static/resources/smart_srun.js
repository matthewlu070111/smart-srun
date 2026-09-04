(function() {
  if (window.__smartSrunUiLoaded) return;
  window.__smartSrunUiLoaded = true;

  var campusData = [];
  var hotspotData = [];
  var modalType = '';
  var modalEditId = '';
  var modalSaveHandler = null;
  var RELEASES_PAGE_URL = 'https://github.com/matthewlu070111/smart-srun/releases';
  var UPDATE_CHECK_URL = '/cgi-bin/luci/admin/services/smart_srun/update_check';
  var UPDATE_START_URL = '/cgi-bin/luci/admin/services/smart_srun/update_start';
  var UPDATE_STATUS_URL = '/cgi-bin/luci/admin/services/smart_srun/update_status';

  function readText(id) {
    var node = document.getElementById(id);
    if (!node) return '';
    return node.value || node.textContent || '';
  }

  function readJson(id, fallbackValue) {
    try {
      var text = readText(id);
      return text ? JSON.parse(text) : fallbackValue;
    } catch (err) {
      return fallbackValue;
    }
  }

  function escapeHtml(value) {
    return String(value == null ? '' : value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function logLineLevel(line) {
    if (line.indexOf('[错误]') !== -1) return 'error';
    if (line.indexOf('[警告]') !== -1) return 'warn';
    if (line.indexOf('[调试]') !== -1) return 'debug';
    if (line.indexOf('[信息]') !== -1) return 'info';
    return 'info';
  }

  var LOG_LEVEL_COLORS = {
    error: '#ff6b6b',
    warn:  '#ffb454',
    debug: '#6c7a89',
    info:  '#9ef19e'
  };

  function renderFriendlyLogHtml(text) {
    var lines = String(text || '').split('\n');
    var out = [];
    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      if (line === '') {
        out.push('');
        continue;
      }
      var level = logLineLevel(line);
      var color = LOG_LEVEL_COLORS[level] || LOG_LEVEL_COLORS.info;
      var weight = (level === 'error' || level === 'warn') ? '600' : '400';
      var opacity = (level === 'debug') ? '0.78' : '1';
      out.push(
        '<span style="color:' + color + ';font-weight:' + weight +
        ';opacity:' + opacity + ';">' + escapeHtml(line) + '</span>'
      );
    }
    return out.join('\n');
  }

  function fetchJson(url, callback) {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', url, true);
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      if (xhr.status !== 200) {
        callback(new Error('http_' + xhr.status));
        return;
      }
      try {
        callback(null, JSON.parse(xhr.responseText || '{}'));
      } catch (err) {
        callback(err);
      }
    };
    xhr.send(null);
  }

  function isPageHidden() {
    return document.hidden === true || document.webkitHidden === true;
  }

  function onPageVisible(callback) {
    function runIfVisible() {
      if (!isPageHidden()) callback();
    }
    document.addEventListener('visibilitychange', runIfVisible, false);
    document.addEventListener('webkitvisibilitychange', runIfVisible, false);
  }

  function formatUpdateStatus(data) {
    data = data || {};
    var lines = [];
    lines.push('状态：' + (data.message || data.phase || '未知'));
    if (data.current_version) lines.push('当前版本：' + data.current_version);
    if (data.latest_tag || data.latest_version) lines.push('目标版本：' + (data.latest_tag || data.latest_version));
    if (data.install_mode) lines.push('包型：' + data.install_mode + ' / ' + (data.package_format || ''));
    if (data.package_name) lines.push('当前包：' + data.package_name);
    if (data.asset_name) lines.push('下载项：' + data.asset_name);
    return lines.join('\n');
  }

  function pollUpdateStatus(outputNode, errorStreak) {
    errorStreak = errorStreak || 0;
    fetchJson(UPDATE_STATUS_URL, function(err, data) {
      if (err || !data) {
        // 更新末尾会重启 uwsgi，期间状态接口短暂不可用属正常现象，
        // 容忍几次失败后再放弃，避免误报“读取更新状态失败”。
        if (errorStreak >= 5) {
          outputNode.textContent = '读取更新状态失败';
          return;
        }
        outputNode.textContent = '更新进行中，正在等待服务恢复…';
        setTimeout(function() { pollUpdateStatus(outputNode, errorStreak + 1); }, 2000);
        return;
      }
      outputNode.textContent = formatUpdateStatus(data);
      if (data.running) {
        setTimeout(function() { pollUpdateStatus(outputNode, 0); }, 2000);
      }
    });
  }

  function openUpdateModal(plan) {
    plan = plan || {};
    var output = E('pre', {
      'style': 'max-height:18rem;overflow:auto;margin:0;padding:.75rem;border:1px solid rgba(127,127,127,.28);background:rgba(127,127,127,.08);white-space:pre-wrap;word-break:break-word;'
    }, formatUpdateStatus(plan));
    var buttonRow = E('div', { 'class': 'right' });
    var cancelBtn = E('button', {
      'type': 'button',
      'class': 'btn cbi-button',
      'click': function() { L.hideModal(); }
    }, '取消');
    var releaseBtn = E('a', {
      'class': 'btn cbi-button',
      'href': plan.release_page || RELEASES_PAGE_URL,
      'target': '_blank',
      'rel': 'noopener noreferrer'
    }, '发布页');
    var updateBtn = E('button', {
      'type': 'button',
      'class': 'btn cbi-button cbi-button-apply important',
      'click': function() {
        var target = plan.latest_tag || plan.latest_version || '最新版本';
        if (!confirm('确认自动更新到 ' + target + '？更新过程中请不要刷新或断电。')) return;
        updateBtn.disabled = true;
        output.textContent = '正在提交后台更新任务...';
        fetchJson(UPDATE_START_URL, function(err, data) {
          if (err || !data) {
            output.textContent = '提交更新失败';
            updateBtn.disabled = false;
            return;
          }
          output.textContent = formatUpdateStatus(data);
          pollUpdateStatus(output);
        });
      }
    }, '自动更新');
    buttonRow.appendChild(cancelBtn);
    buttonRow.appendChild(document.createTextNode(' '));
    buttonRow.appendChild(releaseBtn);
    buttonRow.appendChild(document.createTextNode(' '));
    buttonRow.appendChild(updateBtn);
    L.showModal('SMART SRun 更新', [output, buttonRow], 'cbi-modal');
  }

  function initVersionNotice() {
    var container = document.getElementById('smart-srun-version-info');
    var link = document.getElementById('smart-srun-version-link');
    var dot = document.getElementById('smart-srun-update-dot');
    if (!container || !link || !dot || window.__smartSrunVersionInit) return;
    window.__smartSrunVersionInit = true;

    link.href = RELEASES_PAGE_URL;
    var updatePlan = null;
    link.addEventListener('click', function(ev) {
      if (!updatePlan || !updatePlan.update_available) return;
      ev.preventDefault();
      openUpdateModal(updatePlan);
    });

    fetchJson(UPDATE_CHECK_URL, function(err, data) {
      if (err || !data || !data.ok || !data.update_available) return;
      updatePlan = data;
      dot.style.display = 'inline-block';
      link.title = '发现新版本：' + (data.latest_tag || data.latest_version || '');
    });
  }

  window.smartFetchJson = fetchJson;

  function renderPortalGuidance(container, url) {
    if (!container) return;
    container.textContent = '';
    container.style.display = 'none';
    url = String(url || '');
    // Only open a configured HTTP(S) school page on an explicit user click.
    if (!/^https?:\/\//i.test(url) || /[\s\\]/.test(url)) return;
    var link = document.createElement('a');
    link.href = url;
    if ((link.protocol !== 'http:' && link.protocol !== 'https:') || !link.host) return;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    link.textContent = '打开学校认证页';
    container.appendChild(link);
    container.appendChild(document.createTextNode('，确认网络状态后可返回重试。'));
    container.style.display = '';
  }

  function openBlockingFeedback(action, requestedAt) {
    var result = document.getElementById('smart-srun-manual-result') || document.getElementById('smart-srun-switch-result');
    var resultPortal = document.getElementById('smart-srun-manual-portal');
    renderPortalGuidance(resultPortal, '');
    var logBox = E('pre', {
      'style': 'max-height:18rem;overflow:auto;margin:0;padding:.75rem;border:1px solid rgba(127,127,127,.28);background:rgba(127,127,127,.08);white-space:pre-wrap;word-break:break-word;'
    }, '等待后端反馈...');
    var titles = {
      manual_login: '正在登录',
      manual_logout: '正在登出',
      switch_hotspot: '正在切到热点',
      switch_campus: '正在切回校园网'
    };
    var tips = {
      manual_login: '正在执行登录流程，请勿关闭页面。',
      manual_logout: '正在执行登出流程，请稍候。',
      switch_hotspot: '正在切换到热点网络，请稍候。',
      switch_campus: '正在切换回校园网，请稍候。'
    };
    var tip = E('p', { 'style': 'margin:.5rem 0 1rem 0;' }, tips[action] || '正在执行网络动作，请稍候。');
    var portalHelp = E('div', { 'style': 'display:none;margin:.75rem 0;' });
    var footer = E('div', { 'class': 'right' });
    var closed = false;
    var timer = null;
    var progressButton = E('button', {
      'class': 'btn cbi-button',
      'disabled': 'disabled'
    }, '进行中');
    var forceButton = E('button', {
      'class': 'btn cbi-button cbi-button-remove',
      'click': function(ev) {
        ev.preventDefault();
        if (closed || forceButton.disabled) return;
        forceButton.disabled = true;
        if (result) result.textContent = '正在强制停止...';
        var xhr = new XMLHttpRequest();
        xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
        xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
        xhr.onreadystatechange = function() {
          if (xhr.readyState !== 4) return;
          var text = '已触发强制停止';
          if (xhr.status === 200) {
            try {
              var data = JSON.parse(xhr.responseText || '{}');
              if (typeof data.message === 'string' && data.message !== '')
                text = data.message;
            } catch (e) {}
          }
          unlock(text, false);
        };
        xhr.send('action=' + encodeURIComponent('force_stop'));
      }
    }, '强制停止');

    progressButton.addEventListener('click', function(ev) {
      if (progressButton.disabled) {
        ev.preventDefault();
        return;
      }
      L.hideModal();
      location.reload();
    });

    footer.appendChild(progressButton);
    footer.appendChild(forceButton);

    function setTerminalFooter() {
      progressButton.disabled = false;
      progressButton.textContent = '关闭返回';
      forceButton.disabled = true;
    }

    function unlock(text, success, portalUrl) {
      if (closed) return;
      closed = true;
      if (timer) window.clearInterval(timer);
      setTerminalFooter();
      tip.textContent = text || (success ? '操作完成' : '执行失败');
      renderPortalGuidance(portalHelp, success ? '' : portalUrl);
      renderPortalGuidance(resultPortal, success ? '' : portalUrl);
      if (result && text) result.textContent = text + (success ? ' 🎉' : ' ⚠');
    }

    function checkTerminal(statusData) {
      if (!statusData) return false;
      if (statusData.last_action !== action) return false;
      if ((statusData.last_action_ts || 0) < requestedAt) return false;
      if (statusData.action_result === 'forced') {
        unlock(statusData.last_action_message || statusData.status || '已强制停止', false);
        return true;
      }
      if (statusData.action_result === 'error') {
        unlock(statusData.last_action_message || statusData.status || '执行失败', false, statusData.last_action_portal_url);
        return true;
      }
      if (statusData.action_result === 'ok') {
        unlock(statusData.last_action_message || statusData.status || '操作完成', true);
        return true;
      }
      return false;
    }

    function poll() {
      fetchJson('/cgi-bin/luci/admin/services/smart_srun/log_tail?lines=200&format=friendly&since=' + encodeURIComponent(requestedAt) + '&_=' + Date.now(), function(err, logData) {
        if (!err && logData && typeof logData.log === 'string' && !logData.empty) {
          logBox.innerHTML = renderFriendlyLogHtml(logData.log);
          logBox.scrollTop = logBox.scrollHeight;
        }
      });

      fetchJson('/cgi-bin/luci/admin/services/smart_srun/status?_=' + Date.now(), function(err, statusData) {
        if (err) return;
        checkTerminal(statusData);
      });
    }

    L.showModal(titles[action] || '正在执行动作', [ tip, logBox, portalHelp, footer ], 'cbi-modal');
    timer = window.setInterval(poll, 1000);
    poll();
  }

  window.smartOpenBlockingFeedback = openBlockingFeedback;

  function getFieldValue(id) {
    var node = document.getElementById('widget.' + id) || document.getElementById(id);
    return node ? node.value : '';
  }

  function renderPasswordField(containerId, fieldId, value) {
    var container = document.getElementById(containerId);
    if (!container) return;
    L.require('ui').then(function(ui) {
      var widget = new ui.Textfield(value || '', {
        id: fieldId,
        password: true,
        optional: true
      });
      return Promise.resolve(widget.render()).then(function(node) {
        container.innerHTML = '';
        container.appendChild(node);
      });
    });
  }

  function setRowDisabled(rowId, inputId, disabled) {
    var row = document.getElementById(rowId);
    var input = document.getElementById(inputId);
    if (!row || !input) return;
    input.disabled = !!disabled;
    row.style.opacity = disabled ? '0.55' : '1';
  }

  function updateCampusAccessModeUI() {
    var mode = document.getElementById('jm-access_mode');
    if (!mode) return;
    var wired = mode.value === 'wired';
    var policy = document.getElementById('jm-ap_selection');
    setRowDisabled('jm-wired-iface-row', 'jm-wired_iface', !wired);
    setRowDisabled('jm-auth-enabled-row', 'jm-auth_enabled', !wired);
    setRowDisabled('jm-ssid-row', 'jm-ssid', wired);
    setRowDisabled('jm-ap-selection-row', 'jm-ap_selection', wired);
    setRowDisabled('jm-bssid-row', 'jm-bssid', wired || !policy || policy.value !== 'fixed');
    setRowDisabled('jm-radio-row', 'jm-radio', wired);
  }

  function normalizeApSelection(value, bssid) {
    var policy = String(value || '').replace(/^\s+|\s+$/g, '').toLowerCase();
    if (policy === 'auto' || policy === 'strongest' || policy === 'fixed') return policy;
    return String(bssid || '').replace(/^\s+|\s+$/g, '') ? 'fixed' : 'auto';
  }

  function isValidBssid(value) {
    var address = String(value || '').replace(/^\s+|\s+$/g, '').toLowerCase();
    return /^(?:[0-9a-f]{2}:){5}[0-9a-f]{2}$/.test(address) &&
      address !== '00:00:00:00:00:00' && (parseInt(address.slice(0, 2), 16) & 1) === 0;
  }

  function apSelectionLabel(policy) {
    var labels = { auto: '系统自动', strongest: '连接时信号优先', fixed: '固定 BSSID' };
    return labels[policy] || '未知';
  }

  function wirelessStatusMarkup(data) {
    if (data.current_campus_access_mode === 'wired' || data.mode_label === '校园网模式（有线）') return '';
    function observed(value, suffix) {
      return value === undefined || value === null || value === '' ? '未知' : escapeHtml(value) + (suffix || '');
    }
    var markup = '<span>实际 AP: ' + observed(data.current_bssid) + '</span>' +
      '<span>无线接口: ' + observed(data.current_wireless_ifname) + '</span>' +
      '<span>信号: ' + observed(data.current_signal, ' dBm') + '</span>' +
      '<span>信道: ' + observed(data.current_channel) + '</span>';
    if (data.mode !== 'hotspot' && data.current_mode !== 'hotspot' && data.mode_label !== '热点模式') {
      markup += '<span>AP 选择: ' + escapeHtml(apSelectionLabel(data.ap_selection_policy)) + '</span>' +
        '<span>选择说明: ' + observed(data.ap_selection_reason) + '</span>';
    }
    return markup;
  }

  function showNativeModal(title, bodyHtml, afterOpen, onSave) {
    var body = document.createElement('div');
    body.innerHTML = bodyHtml;

    var buttonRow = document.createElement('div');
    buttonRow.className = 'right';

    var cancelBtn = document.createElement('button');
    cancelBtn.type = 'button';
    cancelBtn.className = 'btn cbi-button';
    cancelBtn.textContent = '取消';
    cancelBtn.onclick = function() { L.hideModal(); };

    var saveBtn = document.createElement('button');
    saveBtn.type = 'button';
    saveBtn.className = 'btn cbi-button cbi-button-save important';
    saveBtn.textContent = '保存';
    saveBtn.onclick = function() {
      if (typeof modalSaveHandler === 'function') modalSaveHandler();
    };

    buttonRow.appendChild(cancelBtn);
    buttonRow.appendChild(document.createTextNode(' '));
    buttonRow.appendChild(saveBtn);

    modalSaveHandler = onSave;
    L.showModal(title, [ body, buttonRow ], 'cbi-modal');
    if (typeof afterOpen === 'function') afterOpen();
  }

  function findById(items, id) {
    for (var i = 0; i < items.length; i++) {
      if (items[i].id === id) return items[i];
    }
    return null;
  }

  function schoolPresetList() {
    // 不再内置任何学校的兜底预设：预设列表完全来自后端（远端/缓存/打包 fallback）。
    var items = readJson('smart-school-preset-data', []);
    var active = [];
    for (var i = 0; items && i < items.length; i++) {
      if (items[i] && items[i].status === 'active') active.push(items[i]);
    }
    return active;
  }

  function refreshSchoolPresets() {
    var node = document.getElementById('smart-school-preset-data');
    if (!node || window.__smartPresetsRefresh) return;
    window.__smartPresetsRefresh = true;
    fetchJson('/cgi-bin/luci/admin/services/smart_srun/presets_refresh?_=' + Date.now(), function(err, data) {
      if (err || !data || !data.ok || !data.schools) return;
      node.value = JSON.stringify(data.schools);
      node.textContent = node.value;
    });
  }

  var DEFAULT_LOGIN_SHAPE = {
    n: '200',
    type: '1',
    enc: 'srun_bx1',
    info_prefix: 'SRBX1',
    double_stack: '0',
    os: 'Windows 10',
    name: 'Windows'
  };

  function findSchoolPreset(id) {
    // 无预设（空/哨兵值）或找不到时一律返回 null，不再回落到任何特定学校。
    // 查找范围包含用户自定义预设（路由器侧 user_presets.json）。
    var wanted = String(id || '');
    if (!wanted || wanted === '__none__') return null;
    var items = schoolPresetList().concat(loadCustomPresets());
    for (var i = 0; i < items.length; i++) {
      if (String(items[i].short_name || '') === wanted) return items[i];
    }
    return null;
  }

  // 用户自定义预设/运营商存储：真身在路由器侧 /usr/lib/smart_srun/user_presets.json，
  // 页面渲染时经 #smart-user-preset-data 注入，增删后整份 POST 回写，跨设备共享。
  var USER_PRESETS_SET_URL = '/cgi-bin/luci/admin/services/smart_srun/user_presets_set';
  var userPresetStore = { presets: [], operators: [] };

  function normalizeUserStore(raw) {
    var store = { presets: [], operators: [] };
    if (raw && raw.presets && raw.presets.length) store.presets = raw.presets;
    if (raw && raw.operators && raw.operators.length) store.operators = raw.operators;
    return store;
  }

  function initUserPresetStore() {
    userPresetStore = normalizeUserStore(readJson('smart-user-preset-data', null));
  }

  function pushUserPresetStore(callback) {
    var fd = new FormData();
    fd.append('data', JSON.stringify(userPresetStore));
    var xhr = new XMLHttpRequest();
    xhr.open('POST', USER_PRESETS_SET_URL, true);
    xhr.onload = function() {
      var data = {};
      try { data = JSON.parse(xhr.responseText || '{}'); } catch (e) {}
      if (xhr.status === 200 && data.ok) {
        if (callback) callback(null);
      } else {
        if (callback) callback(new Error((data && data.message) ? data.message : ('HTTP ' + xhr.status)));
      }
    };
    xhr.onerror = function() {
      if (callback) callback(new Error('网络错误'));
    };
    xhr.send(fd);
  }

  // 返回存储内的原数组引用：调用方就地增删后调 pushUserPresetStore 落盘。
  function loadCustomPresets() {
    return userPresetStore.presets;
  }

  function loadCustomOperators() {
    return userPresetStore.operators;
  }

  function saveCustomOperators(ops) {
    userPresetStore.operators = ops || [];
    pushUserPresetStore(function(err) {
      if (err) alert('运营商列表已在本页生效，但保存到路由器失败：' + err.message);
    });
  }

  function isCustomPresetId(id) {
    return String(id || '').indexOf('custom-') === 0;
  }

  function radioOptionsMarkup() {
    return readText('smart-radio-options');
  }

  window.smartSetDefault = function(kind, id) {
    var fd = new FormData();
    fd.append('action', 'set_default_' + kind);
    fd.append('id', id);
    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.onload = function() {
      var data = {};
      try { data = JSON.parse(xhr.responseText || '{}'); } catch (e) {}
      if (xhr.status === 200 && data.ok !== false) {
        alert((typeof data.message === 'string' && data.message !== '') ? data.message : '已保存默认配置');
        location.reload();
      } else {
        alert((data && data.message) ? data.message : ('操作失败（HTTP ' + xhr.status + '）'));
      }
    };
    xhr.onerror = function() { alert('操作失败：网络错误，请重试'); };
    xhr.send(fd);
  };

  window.smartDelete = function(kind, id) {
    if (!confirm('确定要删除此项吗？')) return;
    var fd = new FormData();
    fd.append('action', 'delete_' + kind);
    fd.append('id', id);
    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.onload = function() {
      var data = {};
      try { data = JSON.parse(xhr.responseText || '{}'); } catch (e) {}
      if (xhr.status === 200 && data.ok !== false) {
        location.reload();
      } else {
        alert((data && data.message) ? data.message : ('删除失败（HTTP ' + xhr.status + '）'));
      }
    };
    xhr.onerror = function() { alert('删除失败：网络错误，请重试'); };
    xhr.send(fd);
  };

  window.smartEditCampus = function(id) {
    modalType = 'campus';
    modalEditId = id;
    var item = id ? findById(campusData, id) : {};
    var presets = schoolPresetList();
    var customPresets = loadCustomPresets();
    var NO_PRESET_ID = '__none__';
    // 默认不选任何学校预设：预设只负责预填写，所有字段都由用户决定。
    var selectedPresetId = NO_PRESET_ID;
    var initialValues = {
      label: item.label || '',
      user_id: item.user_id || '',
      operator_suffix: item.operator_suffix || '',
      access_mode: item.access_mode || 'wifi',
      wired_iface: String(item.wired_iface || '').replace(/^\s+|\s+$/g, '') || String(item.network_interface || '').replace(/^\s+|\s+$/g, '') || 'wan',
      auth_enabled: String(item.auth_enabled || '0'),
      base_url: item.base_url || '',
      ac_id: item.ac_id || '1',
      n: item.n || '',
      type: item.type || '',
      enc: item.enc || '',
      info_prefix: item.info_prefix || '',
      double_stack: item.double_stack || '',
      login_os: item.login_os || '',
      login_name: item.login_name || '',
      ssid: item.ssid || '',
      bssid: item.bssid || '',
      ap_selection: normalizeApSelection(item.ap_selection, item.bssid),
      radio: item.radio || ''
    };

    // 运营商快捷下拉是一份可编辑列表：学校预设的 operators 只作预填写（可删），
    // 用户自增的条目落在路由器侧 user_presets.json，跨设备共享。
    // 字段已从 id 改名为 suffix；仍兼容旧的 id 键。
    function operatorSuffixOf(op) {
      if (!op) return '';
      var v = (op.suffix !== undefined && op.suffix !== null) ? op.suffix : op.id;
      return String(v || '');
    }

    var operatorChoices = [];

    function operatorChoiceExists(suffix, label) {
      for (var i = 0; i < operatorChoices.length; i++) {
        if (operatorChoices[i].suffix === suffix && operatorChoices[i].label === label) return true;
      }
      return false;
    }

    function addOperatorChoice(suffix, label, custom) {
      suffix = String(suffix || '');
      label = String(label || '') || (suffix === '' ? '无后缀' : suffix);
      if (operatorChoiceExists(suffix, label)) return;
      operatorChoices.push({ suffix: suffix, label: label, custom: !!custom });
    }

    function persistCustomOperators() {
      var out = [];
      for (var i = 0; i < operatorChoices.length; i++) {
        if (operatorChoices[i].custom) out.push({ suffix: operatorChoices[i].suffix, label: operatorChoices[i].label });
      }
      saveCustomOperators(out);
    }

    function seedOperatorChoices() {
      operatorChoices = [];
      var saved = loadCustomOperators();
      for (var i = 0; i < saved.length; i++) {
        addOperatorChoice(saved[i].suffix, saved[i].label, true);
      }
      var current = String(initialValues.operator_suffix || '');
      if (current) addOperatorChoice(current, current, false);
    }

    function renderOperatorChoices(selectedSuffix) {
      var opSel = document.getElementById('jm-operator');
      if (!opSel) return;
      var out = '';
      if (!operatorChoices.length) {
        out = '<option value="" disabled selected>（暂无选项，点“添加”自建）</option>';
      }
      for (var oi = 0; oi < operatorChoices.length; oi++) {
        var sfx = operatorChoices[oi].suffix;
        // suffix 为 "??" 表示该运营商后缀尚未被提供者验证，下拉里标注“未验证”。
        var text = (sfx === '??') ? (operatorChoices[oi].label + '（未验证）') : operatorChoices[oi].label;
        out += '<option value="' + escapeHtml(sfx) + '">' + escapeHtml(text) + '</option>';
      }
      opSel.innerHTML = out;
      if (operatorChoices.length && selectedSuffix !== undefined && selectedSuffix !== null) {
        opSel.value = String(selectedSuffix);
      }
    }

    // 应用预设时用该校 operators 整体替换下拉中的「预设预填」项。
    // 上一所学校留下的 label/suffix（如南航「学生用户」）必须清掉，否则会串到
    // 下一所学校；用户自建（custom）条目仍保留（与「复位」一致）。
    function replacePresetOperators(preset) {
      var kept = [];
      for (var i = 0; i < operatorChoices.length; i++) {
        if (operatorChoices[i].custom) kept.push(operatorChoices[i]);
      }
      operatorChoices = kept;
      var ops = (preset && preset.operators && preset.operators.length) ? preset.operators : [];
      for (var j = 0; j < ops.length; j++) {
        addOperatorChoice(operatorSuffixOf(ops[j]), String(ops[j].label || ''), false);
      }
      return ops;
    }

    // 下拉选择运营商 -> 填充后缀输入框。"??"（未验证）不直接写入，而是清空并提示用户手填。
    function applyOperatorPick() {
      var sfx = document.getElementById('jm-operator_suffix');
      var opSel = document.getElementById('jm-operator');
      var hint = document.getElementById('jm-operator-suffix-hint');
      if (!sfx || !opSel) return;
      var val = String(opSel.value || '');
      if (val === '??') {
        sfx.value = '';
        if (hint) {
          hint.textContent = '该运营商后缀尚未被验证，请自行确认后手动填写。';
          hint.style.display = '';
        }
      } else {
        sfx.value = val;
        if (hint) {
          hint.textContent = '';
          hint.style.display = 'none';
        }
      }
    }

    function addOperatorFromPrompt() {
      var label = window.prompt('运营商显示名称（例如：学生用户 / 中国移动）', '');
      if (label === null) return;
      var sfx = window.prompt('运营商后缀（用户名 @ 后面的部分，留空表示纯账号不带后缀）', '');
      if (sfx === null) return;
      label = String(label).replace(/^\s+|\s+$/g, '');
      sfx = String(sfx).replace(/^\s+|\s+$/g, '').replace(/^@+/, '');
      addOperatorChoice(sfx, label, true);
      persistCustomOperators();
      renderOperatorChoices(sfx);
      applyOperatorPick();
    }

    function removeSelectedOperator() {
      var opSel = document.getElementById('jm-operator');
      if (!opSel || !operatorChoices.length) return;
      var idx = opSel.selectedIndex;
      if (idx < 0 || idx >= operatorChoices.length) return;
      operatorChoices.splice(idx, 1);
      persistCustomOperators();
      var nextIdx = Math.min(idx, operatorChoices.length - 1);
      renderOperatorChoices(nextIdx >= 0 ? operatorChoices[nextIdx].suffix : undefined);
    }

    function presetOptionsMarkup() {
      // 自定义预设置顶：无预设 → 自定义 → 远端/内置。
      var out = '<option value="' + NO_PRESET_ID + '"' + (selectedPresetId === NO_PRESET_ID ? ' selected' : '') + '>无预设</option>';
      for (var ci = 0; ci < customPresets.length; ci++) {
        var customId = String(customPresets[ci].short_name || '');
        if (!customId) continue;
        out += '<option value="' + escapeHtml(customId) + '"' + (customId === selectedPresetId ? ' selected' : '') + '>' + escapeHtml(String(customPresets[ci].name || customId)) + '（自定义）</option>';
      }
      for (var pi = 0; pi < presets.length; pi++) {
        var presetId = String(presets[pi].short_name || '');
        if (!presetId) continue;
        out += '<option value="' + escapeHtml(presetId) + '"' + (presetId === selectedPresetId ? ' selected' : '') + '>' + escapeHtml(presets[pi].name || presetId) + '</option>';
      }
      return out;
    }

    function rebuildPresetSelect() {
      var presetSel = document.getElementById('jm-school_preset');
      if (presetSel) presetSel.innerHTML = presetOptionsMarkup();
    }

    function formFieldValue(idKey) {
      var node = document.getElementById(idKey);
      return node ? String(node.value || '') : '';
    }

    // 把当前表单的环境字段存为自定义预设（路由器侧 user_presets.json）。
    // 只存环境信息（认证地址/AC_ID/SSID/接入方式/登录形态/运营商列表），
    // 学工号、密码等凭据绝不写入。
    function saveCurrentFormAsPreset() {
      var name = window.prompt('预设名称（与已有自定义预设同名会覆盖它）', '');
      if (name === null) return;
      name = String(name).replace(/^\s+|\s+$/g, '');
      if (!name) {
        alert('预设名称不能为空');
        return;
      }
      var ops = [];
      for (var i = 0; i < operatorChoices.length; i++) {
        ops.push({ suffix: operatorChoices[i].suffix, label: operatorChoices[i].label });
      }
      var preset = {
        short_name: 'custom-' + new Date().getTime(),
        name: name,
        custom: true,
        defaults: {
          base_url: formFieldValue('jm-base_url'),
          ac_id: formFieldValue('jm-ac_id'),
          ssid: formFieldValue('jm-ssid'),
          access_mode: formFieldValue('jm-access_mode'),
          wired_iface: formFieldValue('jm-wired_iface')
        },
        observed_login_shape: {
          n: formFieldValue('jm-login-n'),
          type: formFieldValue('jm-login-type'),
          enc: formFieldValue('jm-login-enc'),
          info_prefix: formFieldValue('jm-info-prefix'),
          double_stack: formFieldValue('jm-double-stack'),
          os: formFieldValue('jm-login-os'),
          name: formFieldValue('jm-login-name')
        },
        operators: ops
      };
      for (var pi = 0; pi < customPresets.length; pi++) {
        if (String(customPresets[pi].name || '') === name) {
          preset.short_name = String(customPresets[pi].short_name || preset.short_name);
          customPresets.splice(pi, 1);
          break;
        }
      }
      customPresets.push(preset);
      selectedPresetId = preset.short_name;
      rebuildPresetSelect();
      pushUserPresetStore(function(err) {
        if (err) alert('预设已在本页生效，但保存到路由器失败：' + err.message);
        else alert('已保存自定义预设：' + name);
      });
    }

    function deleteSelectedCustomPreset() {
      var presetSel = document.getElementById('jm-school_preset');
      var pid = presetSel ? String(presetSel.value || '') : '';
      if (!isCustomPresetId(pid)) {
        alert('只能删除自己保存的自定义预设');
        return;
      }
      var targetIdx = -1;
      for (var i = 0; i < customPresets.length; i++) {
        if (String(customPresets[i].short_name || '') === pid) {
          targetIdx = i;
          break;
        }
      }
      if (targetIdx < 0) {
        alert('未找到该自定义预设');
        return;
      }
      if (!confirm('确定删除自定义预设「' + String(customPresets[targetIdx].name || pid) + '」？')) return;
      customPresets.splice(targetIdx, 1);
      selectedPresetId = NO_PRESET_ID;
      rebuildPresetSelect();
      pushUserPresetStore(function(err) {
        if (err) alert('预设已在本页删除，但保存到路由器失败：' + err.message);
      });
    }

    var bodyHtml =
      '<div class="smart-native-row"><label>学校预设</label><span><select id="jm-school_preset">' + presetOptionsMarkup() + '</select> <button type="button" id="jm-apply-school-defaults" class="btn cbi-button cbi-button-action">应用预设</button> <button type="button" id="jm-reset-school-defaults" class="btn cbi-button">复位</button></span></div>' +
      '<div class="smart-native-row"><label>标签（选填）</label><input id="jm-label" value="' + escapeHtml(initialValues.label) + '"></div>' +
      '<div class="smart-native-row"><label>学工号</label><input id="jm-user_id" value="' + escapeHtml(initialValues.user_id) + '"></div>' +
      '<div class="smart-native-row"><label>密码</label><div id="jm-password-field"></div></div>' +
      '<div class="smart-native-row"><label>运营商后缀 <a href="https://github.com/matthewlu070111/smart-srun#%E8%8E%B7%E5%8F%96%E5%AD%A6%E6%A0%A1%E9%A2%84%E8%AE%BE%E4%B8%8E%E7%8E%AF%E5%A2%83%E7%9C%9F%E5%AE%9E%E5%AD%97%E6%AE%B5%E5%80%BC" target="_blank" rel="noopener noreferrer">如何获取？</a></label>' +
        '<span id="jm-operator-quickpick-wrap" style="display:flex;gap:6px;align-items:center;margin-bottom:.35rem;">' +
          '<select id="jm-operator" style="flex:1 1 auto;width:auto;"></select>' +
          '<button type="button" id="jm-operator-add" class="btn cbi-button" style="flex:0 0 auto;">添加</button>' +
          '<button type="button" id="jm-operator-del" class="btn cbi-button cbi-button-remove" style="flex:0 0 auto;">删除</button>' +
        '</span>' +
        '<input id="jm-operator_suffix" value="' + escapeHtml(initialValues.operator_suffix) + '" placeholder="">' +
        '<div id="jm-operator-suffix-hint" style="display:none;color:#d97706;font-size:12px;margin-top:.25rem;"></div></div>' +
      '<div class="smart-native-row"><label>接入方式</label><select id="jm-access_mode"><option value="wifi"' + (initialValues.access_mode === 'wifi' ? ' selected' : '') + '>无线</option><option value="wired"' + (initialValues.access_mode === 'wired' ? ' selected' : '') + '>有线（指定接口）</option></select></div>' +
      '<div class="smart-native-row" id="jm-wired-iface-row"><label>有线接口</label><input id="jm-wired_iface" value="' + escapeHtml(initialValues.wired_iface) + '" placeholder="wan.v2"><div style="color:#6b7280;font-size:12px;margin-top:.25rem;">填写 OpenWrt 逻辑接口名或 Linux 设备名；</div></div>' +
      '<div class="smart-native-row" id="jm-auth-enabled-row"><label>参与并行守护</label><select id="jm-auth_enabled"><option value="0"' + (initialValues.auth_enabled !== '1' ? ' selected' : '') + '>关闭</option><option value="1"' + (initialValues.auth_enabled === '1' ? ' selected' : '') + '>启用</option></select><div style="color:#6b7280;font-size:12px;margin-top:.25rem;">需同时开启页面上方“多 WAN 并行认证”；守护进程会按本账号的接口、学工号、密码和后缀独立认证。</div></div>' +
      '<div class="smart-native-row"><label>认证地址</label><input id="jm-base_url" value="' + escapeHtml(initialValues.base_url) + '"></div>' +
      '<div class="smart-native-row"><label>AC_ID</label><span><input id="jm-ac_id" value="' + escapeHtml(initialValues.ac_id) + '"> <button type="button" id="jm-detect-acid" class="btn cbi-button">嗅探</button> <span id="jm-detect-acid-status" style="margin-left:6px;color:#6b7280;"></span></span></div>' +
      '<div class="smart-native-row" id="jm-ssid-row"><label>校园网 SSID</label><input id="jm-ssid" value="' + escapeHtml(initialValues.ssid) + '"></div>' +
      '<details class="smart-native-advanced"><summary>进阶设置</summary>' +
      '<div class="smart-native-row" id="jm-ap-selection-row"><label>AP 选择</label><select id="jm-ap_selection"><option value="auto">系统自动</option><option value="strongest">连接时信号优先</option><option value="fixed">固定 BSSID</option></select></div>' +
      '<div class="smart-native-row" id="jm-radio-row"><label>频段</label><select id="jm-radio">' + radioOptionsMarkup() + '</select></div>' +
      '<div class="smart-native-row" id="jm-bssid-row"><label>固定 BSSID</label><input id="jm-bssid" value="' + escapeHtml(initialValues.bssid) + '" placeholder="02:11:22:33:44:55"></div>' +
      '<p style="color:#6b7280;font-size:12px;">信号优先仅在连接时扫描同 SSID、同一频段的兼容信道；在线不主动漫游，认证失败不会轮换 AP。dBm 越接近 0，信号越强。固定 BSSID 不可用时不会自动改连其他 AP。</p>' +
      '<div class="smart-native-row"><label>n</label><input id="jm-login-n" value="' + escapeHtml(initialValues.n) + '" placeholder="200"></div>' +
      '<div class="smart-native-row"><label>type</label><input id="jm-login-type" value="' + escapeHtml(initialValues.type) + '" placeholder="1"></div>' +
      '<div class="smart-native-row"><label>enc</label><input id="jm-login-enc" value="' + escapeHtml(initialValues.enc) + '" placeholder="srun_bx1"></div>' +
      '<div class="smart-native-row"><label>info 前缀</label><input id="jm-info-prefix" value="' + escapeHtml(initialValues.info_prefix) + '" placeholder="SRBX1"></div>' +
      '<div class="smart-native-row"><label>double_stack</label><input id="jm-double-stack" value="' + escapeHtml(initialValues.double_stack) + '" placeholder="0"></div>' +
      '<div class="smart-native-row"><label>os</label><input id="jm-login-os" value="' + escapeHtml(initialValues.login_os) + '" placeholder="Windows 10"></div>' +
      '<div class="smart-native-row"><label>name</label><input id="jm-login-name" value="' + escapeHtml(initialValues.login_name) + '" placeholder="Windows"></div>' +
      '</details>' +
      '<div style="margin-top:1rem;padding-top:.75rem;border-top:1px solid rgba(127,127,127,.25);display:flex;gap:8px;">' +
        '<button type="button" id="jm-save-school-preset" class="btn cbi-button">保存为新预设</button>' +
        '<button type="button" id="jm-delete-school-preset" class="btn cbi-button cbi-button-remove">删除预设</button>' +
      '</div>';

    function applySchoolDefaultsToForm() {
      var preset = findSchoolPreset(selectedPresetId);
      if (!preset) {
        resetSchoolDefaultsForm();
        return;
      }
      var schoolDefaults = (preset && preset.defaults) ? preset.defaults : {};
      var loginShape = (preset && preset.observed_login_shape) ? preset.observed_login_shape : {};
      var fieldMap = {
        base_url: 'jm-base_url',
        ac_id: 'jm-ac_id',
        ssid: 'jm-ssid'
      };
      for (var key in fieldMap) {
        var target = document.getElementById(fieldMap[key]);
        if (!target) continue;
        target.value = (schoolDefaults[key] !== undefined && schoolDefaults[key] !== null) ? String(schoolDefaults[key]) : '';
      }
      var nextOperators = replacePresetOperators(preset);
      var nextSuffix = nextOperators.length ? operatorSuffixOf(nextOperators[0]) : '';
      renderOperatorChoices(nextOperators.length ? nextSuffix : undefined);
      applyLoginShapeToForm(loginShape);
      // 应用预设时：有运营商则用第一个联动填充后缀；无运营商则清空后缀，避免残留旧校值。
      if (nextOperators.length) {
        applyOperatorPick();
      } else {
        var emptySuffix = document.getElementById('jm-operator_suffix');
        if (emptySuffix) emptySuffix.value = '';
        var emptyHint = document.getElementById('jm-operator-suffix-hint');
        if (emptyHint) {
          emptyHint.textContent = '';
          emptyHint.style.display = 'none';
        }
      }
      if (schoolDefaults.access_mode) {
        var modeSel = document.getElementById('jm-access_mode');
        if (modeSel) modeSel.value = String(schoolDefaults.access_mode);
      } else {
        var fallbackModeSel = document.getElementById('jm-access_mode');
        if (fallbackModeSel && selectedPresetId === NO_PRESET_ID) fallbackModeSel.value = 'wifi';
      }
      if (schoolDefaults.wired_iface !== undefined && schoolDefaults.wired_iface !== null) {
        var ifaceInput = document.getElementById('jm-wired_iface');
        if (ifaceInput) ifaceInput.value = String(schoolDefaults.wired_iface || 'wan');
      }
      updateCampusAccessModeUI();
    }

    function applyLoginShapeToForm(shape) {
      shape = shape || {};
      var map = {
        n: 'jm-login-n',
        type: 'jm-login-type',
        enc: 'jm-login-enc',
        info_prefix: 'jm-info-prefix',
        double_stack: 'jm-double-stack'
      };
      for (var key in map) {
        var target = document.getElementById(map[key]);
        if (!target) continue;
        target.value = (shape[key] !== undefined && shape[key] !== null) ? String(shape[key]) : '';
      }
      var osNode = document.getElementById('jm-login-os');
      if (osNode) osNode.value = (shape.os !== undefined && shape.os !== null) ? String(shape.os) : '';
      var nameNode = document.getElementById('jm-login-name');
      if (nameNode) nameNode.value = (shape.name !== undefined && shape.name !== null) ? String(shape.name) : '';
    }

    function resetSchoolDefaultsForm() {
      selectedPresetId = NO_PRESET_ID;
      var presetSel = document.getElementById('jm-school_preset');
      if (presetSel) presetSel.value = selectedPresetId;
      // 复位：丢弃预设预填的运营商条目，保留用户自建（路由器侧存储）的条目。
      var kept = [];
      for (var ki = 0; ki < operatorChoices.length; ki++) {
        if (operatorChoices[ki].custom) kept.push(operatorChoices[ki]);
      }
      operatorChoices = kept;
      renderOperatorChoices();
      var resetHint = document.getElementById('jm-operator-suffix-hint');
      if (resetHint) { resetHint.textContent = ''; resetHint.style.display = 'none'; }
      var values = {
        'jm-label': initialValues.label,
        'jm-user_id': initialValues.user_id,
        'jm-operator_suffix': '',
        'jm-access_mode': 'wifi',
        'jm-wired_iface': initialValues.wired_iface || 'wan',
        'jm-auth_enabled': '0',
        'jm-base_url': '',
        'jm-ac_id': '',
        'jm-login-n': DEFAULT_LOGIN_SHAPE.n,
        'jm-login-type': DEFAULT_LOGIN_SHAPE.type,
        'jm-login-enc': DEFAULT_LOGIN_SHAPE.enc,
        'jm-info-prefix': DEFAULT_LOGIN_SHAPE.info_prefix,
        'jm-double-stack': DEFAULT_LOGIN_SHAPE.double_stack,
        'jm-login-os': DEFAULT_LOGIN_SHAPE.os,
        'jm-login-name': DEFAULT_LOGIN_SHAPE.name,
        'jm-ssid': '',
        'jm-bssid': initialValues.bssid,
        'jm-ap_selection': initialValues.ap_selection,
        'jm-radio': initialValues.radio
      };
      for (var idKey in values) {
        var node = document.getElementById(idKey);
        if (node) node.value = values[idKey];
      }
      updateCampusAccessModeUI();
    }

    function detectAcidForForm() {
      var baseInput = document.getElementById('jm-base_url');
      var acidInput = document.getElementById('jm-ac_id');
      var statusNode = document.getElementById('jm-detect-acid-status');
      var button = document.getElementById('jm-detect-acid');
      var baseUrl = baseInput ? baseInput.value : '';
      if (!baseUrl) {
        alert('请先填写认证地址');
        return;
      }
      if (button) button.disabled = true;
      if (statusNode) statusNode.textContent = '嗅探中...';
      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/detect_acid', true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onload = function() {
        var data = {};
        try {
          data = JSON.parse(xhr.responseText || '{}');
        } catch (e) {}
        var acid = data.acid || data.ac_id || data.value || '';
        if (data.ok && acid) {
          if (acidInput) acidInput.value = acid;
          if (baseInput) baseInput.value = data.detected_url || data.base_url || baseInput.value;
          if (acidInput) acidInput.dispatchEvent(new Event('change', { bubbles: true }));
          if (baseInput) baseInput.dispatchEvent(new Event('change', { bubbles: true }));
          if (statusNode) statusNode.textContent = '已填入 ' + acid;
        } else {
          if (statusNode) statusNode.textContent = data.message || '未发现 AC_ID';
          else alert(data.message || '未发现 AC_ID');
        }
        if (button) button.disabled = false;
      };
      xhr.onerror = function() {
        if (statusNode) statusNode.textContent = '嗅探请求失败';
        if (button) button.disabled = false;
      };
      xhr.send('base_url=' + encodeURIComponent(baseUrl));
    }

    showNativeModal(
      id ? '编辑校园网账号' : '新增校园网账号',
      bodyHtml,
      function() {
        document.getElementById('jm-radio').value = initialValues.radio;
        document.getElementById('jm-ap_selection').value = initialValues.ap_selection;
        document.getElementById('jm-school_preset').addEventListener('change', function() {
          selectedPresetId = this.value || NO_PRESET_ID;
        });
        document.getElementById('jm-apply-school-defaults').addEventListener('click', applySchoolDefaultsToForm);
        document.getElementById('jm-reset-school-defaults').addEventListener('click', resetSchoolDefaultsForm);
        document.getElementById('jm-save-school-preset').addEventListener('click', saveCurrentFormAsPreset);
        document.getElementById('jm-delete-school-preset').addEventListener('click', deleteSelectedCustomPreset);
        document.getElementById('jm-detect-acid').addEventListener('click', detectAcidForForm);
        document.getElementById('jm-access_mode').addEventListener('change', updateCampusAccessModeUI);
        document.getElementById('jm-ap_selection').addEventListener('change', updateCampusAccessModeUI);
        document.getElementById('jm-operator').addEventListener('change', applyOperatorPick);
        document.getElementById('jm-operator-add').addEventListener('click', addOperatorFromPrompt);
        document.getElementById('jm-operator-del').addEventListener('click', removeSelectedOperator);
        seedOperatorChoices();
        renderOperatorChoices(initialValues.operator_suffix);
        if (!id) applySchoolDefaultsToForm();
        updateCampusAccessModeUI();
        renderPasswordField('jm-password-field', 'jm-password', item.password || '');
      },
      function() { window.smartModalSave(); }
    );
  };

  window.smartEditHotspot = function(id) {
    modalType = 'hotspot';
    modalEditId = id;
    var item = id ? findById(hotspotData, id) : {};
    var bodyHtml =
      '<div class="smart-native-row"><label>标签（选填）</label><input id="jm-label" value="' + escapeHtml(item.label || '') + '"></div>' +
      '<div class="smart-native-row"><label>SSID</label><input id="jm-ssid" value="' + escapeHtml(item.ssid || '') + '"></div>' +
      '<div class="smart-native-row"><label>加密方式</label><select id="jm-encryption"><option value="none"' + (item.encryption === 'none' ? ' selected' : '') + '>开放(none)</option><option value="psk"' + (item.encryption === 'psk' ? ' selected' : '') + '>WPA-PSK</option><option value="psk2"' + ((item.encryption === 'psk2' || !item.encryption) ? ' selected' : '') + '>WPA2-PSK</option><option value="psk-mixed"' + (item.encryption === 'psk-mixed' ? ' selected' : '') + '>WPA/WPA2</option><option value="sae"' + (item.encryption === 'sae' ? ' selected' : '') + '>WPA3-SAE</option><option value="sae-mixed"' + (item.encryption === 'sae-mixed' ? ' selected' : '') + '>WPA2/WPA3</option></select></div>' +
      '<div class="smart-native-row"><label>密码</label><div id="jm-key-field"></div></div>' +
      '<div class="smart-native-row"><label>频段</label><select id="jm-radio">' + radioOptionsMarkup() + '</select></div>';
    showNativeModal(
      id ? '编辑热点配置' : '新增热点配置',
      bodyHtml,
      function() {
        document.getElementById('jm-encryption').value = item.encryption || 'psk2';
        document.getElementById('jm-radio').value = item.radio || '';
        renderPasswordField('jm-key-field', 'jm-key', item.key || '');
      },
      function() { window.smartModalSave(); }
    );
  };

  window.smartModalSave = function() {
    if (window.__smartModalSaving) return;
    if (modalType === 'campus' && getFieldValue('jm-access_mode') !== 'wired' &&
        getFieldValue('jm-ap_selection') === 'fixed' && !isValidBssid(getFieldValue('jm-bssid'))) {
      alert('固定 BSSID 需要有效的单播地址，例如 02:11:22:33:44:55');
      return;
    }
    window.__smartModalSaving = true;
    var fd = new FormData();
    fd.append('action', (modalEditId ? 'edit_' : 'add_') + modalType);
    if (modalEditId) fd.append('id', modalEditId);

    if (modalType === 'campus') {
      fd.append('label', document.getElementById('jm-label').value);
      fd.append('user_id', document.getElementById('jm-user_id').value);
      fd.append('operator_suffix', document.getElementById('jm-operator_suffix').value);
      fd.append('access_mode', document.getElementById('jm-access_mode').value);
      fd.append('wired_iface', document.getElementById('jm-wired_iface').value);
      fd.append('auth_enabled', document.getElementById('jm-auth_enabled').value);
      fd.append('password', getFieldValue('jm-password'));
      fd.append('base_url', document.getElementById('jm-base_url').value);
      fd.append('ac_id', document.getElementById('jm-ac_id').value);
      fd.append('n', document.getElementById('jm-login-n').value);
      fd.append('type', document.getElementById('jm-login-type').value);
      fd.append('enc', document.getElementById('jm-login-enc').value);
      fd.append('info_prefix', document.getElementById('jm-info-prefix').value);
      fd.append('double_stack', document.getElementById('jm-double-stack').value);
      fd.append('login_os', document.getElementById('jm-login-os').value);
      fd.append('login_name', document.getElementById('jm-login-name').value);
      fd.append('ssid', document.getElementById('jm-ssid').value);
      fd.append('bssid', document.getElementById('jm-bssid').value);
      fd.append('ap_selection', document.getElementById('jm-ap_selection').value);
      fd.append('radio', document.getElementById('jm-radio').value);
    } else {
      fd.append('label', document.getElementById('jm-label').value);
      fd.append('ssid', document.getElementById('jm-ssid').value);
      fd.append('encryption', document.getElementById('jm-encryption').value);
      fd.append('key', getFieldValue('jm-key'));
      fd.append('radio', document.getElementById('jm-radio').value);
    }

    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.onload = function() {
      var data = {};
      try { data = JSON.parse(xhr.responseText || '{}'); } catch (e) {}
      if (xhr.status === 200 && data.ok !== false) {
        L.hideModal();
        location.reload();
      } else {
        // 失败时保持弹窗打开，避免静默丢弃用户刚填的整套表单。
        window.__smartModalSaving = false;
        alert((data && data.message) ? data.message : ('保存失败（HTTP ' + xhr.status + '）'));
      }
    };
    xhr.onerror = function() {
      window.__smartModalSaving = false;
      alert('保存失败：网络错误，请重试');
    };
    xhr.send(fd);
  };

  function initSchoolInfo() {
    var infoBox = document.getElementById('smart-school-info');
    var docLinkEl = document.getElementById('smart-school-doc-link');
    if (!infoBox || !docLinkEl || window.__smartSchoolInfoInit) return;
    window.__smartSchoolInfoInit = true;

    var DOC_FALLBACK = 'https://github.com/matthewlu070111/smart-srun/tree/main/doc';
    function docUrlFor(value) {
      var items = schoolPresetList();
      for (var i = 0; i < items.length; i++) {
        if (items[i] && items[i].short_name === value && items[i].doc_url) return items[i].doc_url;
      }
      return DOC_FALLBACK;
    }
    var outerDescEl = null;
    for (var parent = infoBox.parentNode; parent; parent = parent.parentNode) {
      if (parent.className && String(parent.className).indexOf('cbi-value-description') >= 0) {
        outerDescEl = parent;
        break;
      }
    }

    function findSchoolSelect() {
      var node = infoBox;
      while (node) {
        if (node.className && String(node.className).indexOf('cbi-value-field') >= 0) {
          var inner = node.querySelector('select');
          if (inner) return inner;
          break;
        }
        node = node.parentNode;
      }
      return document.getElementById('widget.cbid.smart_srun.main.school')
        || document.getElementById('cbid.smart_srun.main.school')
        || document.querySelector('select[name="cbid.smart_srun.main.school"]');
    }

    var sel = findSchoolSelect();
    if (!sel) return;

    function update(value) {
      infoBox.style.display = 'block';
      if (outerDescEl) outerDescEl.style.display = 'block';
      docLinkEl.href = docUrlFor(String(value || ''));
    }

    update(sel.value);
    sel.addEventListener('change', function() { update(sel.value); });
  }

  function initOverview() {
    var root = document.getElementById('smart-srun-overview');
    var title = document.getElementById('smart-srun-overview-title');
    var meta = document.getElementById('smart-srun-overview-meta');
    if (!root || !title || !meta || window.__smartSrunOverviewInit) return;
    window.__smartSrunOverviewInit = true;

    var palette = {
      online: { border: '#2e7d32', bg: 'rgba(46,125,50,.10)', title: '#166534', meta: '#166534' },
      portal: { border: '#ef6c00', bg: 'rgba(239,108,0,.10)', title: '#b45309', meta: '#92400e' },
      limited: { border: '#c62828', bg: 'rgba(198,40,40,.10)', title: '#b91c1c', meta: '#991b1b' },
      offline: { border: '#6b7280', bg: 'rgba(107,114,128,.10)', title: '#374151', meta: '#4b5563' }
    };

    function applyTone(level) {
      var tone = palette[level] || palette.offline;
      root.style.borderLeftColor = tone.border;
      root.style.background = tone.bg;
      title.style.color = tone.title;
      meta.style.color = tone.meta;
    }

    function refreshOverview() {
      fetchJson('/cgi-bin/luci/admin/services/smart_srun/status?_=' + Date.now(), function(err, data) {
        if (err) {
          applyTone('offline');
          title.textContent = '状态读取失败';
          meta.innerHTML = '<span>WiFi: --</span><span>模式: --</span><span>连通性: --</span>';
          return;
        }
        var level = (typeof data.connectivity_level === 'string' && data.connectivity_level !== '') ? data.connectivity_level : 'offline';
        var status = (typeof data.status === 'string' && data.status !== '') ? data.status : '未知';
        var ssid = (typeof data.current_ssid === 'string' && data.current_ssid !== '') ? data.current_ssid : '未连接';
        var mode = (typeof data.mode_label === 'string' && data.mode_label !== '') ? data.mode_label : '未知模式';
        var conn = (typeof data.connectivity === 'string' && data.connectivity !== '') ? data.connectivity : '未知';
        var iface = (typeof data.current_iface === 'string' && data.current_iface !== '') ? data.current_iface : '--';
        var ip = (typeof data.current_ip === 'string' && data.current_ip !== '') ? data.current_ip : '--';
        var pending = (typeof data.pending_action === 'string' && data.pending_action !== '') ? ('；待执行动作: ' + data.pending_action) : '';
        var campusLabel = (typeof data.online_account_label === 'string' && data.online_account_label !== '') ? data.online_account_label : ((typeof data.campus_account_label === 'string' && data.campus_account_label !== '') ? data.campus_account_label : '--');
        var hotspotLabel = (typeof data.hotspot_profile_label === 'string' && data.hotspot_profile_label !== '') ? data.hotspot_profile_label : '--';

        applyTone(level);
        title.textContent = status + pending;
        var metaHtml = '<span>WiFi: ' + escapeHtml(ssid) + '</span><span>模式: ' + escapeHtml(mode) + '</span><span>连通性: ' + escapeHtml(conn) + '</span><span>接口/IP: ' + escapeHtml(iface) + ' / ' + escapeHtml(ip) + '</span>';
        metaHtml += wirelessStatusMarkup(data);
        if (mode === '热点模式') {
          metaHtml += '<span>热点: ' + escapeHtml(hotspotLabel) + '</span>';
        } else {
          metaHtml += '<span>账号: ' + escapeHtml(campusLabel) + '</span>';
        }
        meta.innerHTML = metaHtml;
      });
    }

    refreshOverview();
    window.setInterval(function() {
      if (!isPageHidden()) refreshOverview();
    }, 1200);
    onPageVisible(refreshOverview);
  }

  function initManualActions() {
    var login = document.getElementById('smart-srun-manual-login');
    var logout = document.getElementById('smart-srun-manual-logout');
    var result = document.getElementById('smart-srun-manual-result');
    if (!login || !logout || !result || window.__smartSrunManualInit) return;
    window.__smartSrunManualInit = true;

    function submit(action) {
      result.textContent = '正在提交...';
      login.disabled = true;
      logout.disabled = true;

      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onreadystatechange = function() {
        if (xhr.readyState !== 4) return;
        login.disabled = false;
        logout.disabled = false;
        if (xhr.status !== 200) {
          result.textContent = '提交失败';
          return;
        }
        try {
          var data = JSON.parse(xhr.responseText || '{}');
          var message = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
          result.textContent = message;
          if (data.ok) {
            openBlockingFeedback(action, parseInt(data.requested_at || 0, 10) || 0);
          }
        } catch (e) {
          result.textContent = '提交失败';
        }
      };
      xhr.send('action=' + encodeURIComponent(action));
    }

    login.addEventListener('click', function() { submit('manual_login'); });
    logout.addEventListener('click', function() { submit('manual_logout'); });
  }

  function initSwitchActions() {
    var hotspot = document.getElementById('smart-srun-switch-hotspot');
    var campus = document.getElementById('smart-srun-switch-campus');
    var forceClose = document.getElementById('smart-srun-force-close');
    var result = document.getElementById('smart-srun-switch-result');
    if (!hotspot || !campus || !forceClose || !result || window.__smartSrunSwitchInit) return;
    window.__smartSrunSwitchInit = true;

    function enqueue(action) {
      result.textContent = '正在提交...';
      hotspot.disabled = true;
      campus.disabled = true;
      forceClose.disabled = true;

      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onreadystatechange = function() {
        if (xhr.readyState !== 4) return;
        hotspot.disabled = false;
        campus.disabled = false;
        forceClose.disabled = false;
        if (xhr.status !== 200) {
          result.textContent = '提交失败';
          return;
        }
        try {
          var data = JSON.parse(xhr.responseText || '{}');
          var message = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
          result.textContent = message;
          if (data.ok) {
            openBlockingFeedback(action, parseInt(data.requested_at || 0, 10) || 0);
          }
        } catch (e) {
          result.textContent = '提交失败';
        }
      };
      xhr.send('action=' + encodeURIComponent(action));
    }

    function enqueueForceClose() {
      if (!confirm('这会停止 SMART SRun 服务并终止插件进程，是否继续？')) {
        return;
      }
      result.textContent = '正在强制关闭插件...';
      hotspot.disabled = true;
      campus.disabled = true;
      forceClose.disabled = true;

      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onreadystatechange = function() {
        if (xhr.readyState !== 4) return;
        hotspot.disabled = false;
        campus.disabled = false;
        forceClose.disabled = false;
        if (xhr.status !== 200) {
          result.textContent = '强制关闭失败';
          return;
        }
        try {
          var data = JSON.parse(xhr.responseText || '{}');
          result.textContent = (typeof data.message === 'string' && data.message !== '') ? data.message : '已强制关闭插件';
          if (data.ok) {
            location.reload();
          }
        } catch (e) {
          result.textContent = '强制关闭失败';
        }
      };
      xhr.send('action=' + encodeURIComponent('force_stop'));
    }

    hotspot.addEventListener('click', function() { enqueue('switch_hotspot'); });
    campus.addEventListener('click', function() { enqueue('switch_campus'); });
    forceClose.addEventListener('click', enqueueForceClose);
  }

  function initTables() {
    if (window.__smartTablesInit) return;
    if (!document.getElementById('smart-campus-data') || !document.getElementById('smart-hotspot-data')) return;
    window.__smartTablesInit = true;
    campusData = readJson('smart-campus-data', []);
    hotspotData = readJson('smart-hotspot-data', []);
    initUserPresetStore();
    refreshSchoolPresets();
  }

  var LOG_LEVEL_WEIGHTS = { ALL: 0, DEBUG: 10, INFO: 20, WARN: 30, ERROR: 40 };
  var LOG_LIVE_LINES = 100;
  var LOG_DOWNLOAD_LINES = 0;

  function logLineWeight(line) {
    if (line.indexOf('[错误]') !== -1) return 40;
    if (line.indexOf('[警告]') !== -1) return 30;
    if (line.indexOf('[信息]') !== -1) return 20;
    if (line.indexOf('[调试]') !== -1) return 10;
    return 20;
  }

  function initLogView() {
    var box = document.getElementById('smart-srun-log-box');
    var pre = document.getElementById('smart-srun-log-pre');
    var startButton = document.getElementById('smart-srun-log-start');
    var stopButton = document.getElementById('smart-srun-log-stop');
    var clearButton = document.getElementById('smart-srun-log-clear');
    var downloadButton = document.getElementById('smart-srun-log-download');
    var levelFilter = document.getElementById('smart-srun-log-level-filter');
    if (!box || !pre || !startButton || !stopButton || !clearButton || !downloadButton || window.__smartSrunLogInit) return;
    window.__smartSrunLogInit = true;
    var logState = {
      refreshing: true,
      timer: null,
      rawText: pre.textContent || '',
      displayLevel: levelFilter && levelFilter.value ? String(levelFilter.value).toUpperCase() : 'ALL'
    };
    if (!(logState.displayLevel in LOG_LEVEL_WEIGHTS)) logState.displayLevel = 'ALL';

    function atBottom() {
      return (box.scrollHeight - box.scrollTop - box.clientHeight) < 24;
    }

    function stickBottom() {
      box.scrollTop = box.scrollHeight;
    }

    function filterByLevel(text) {
      var threshold = LOG_LEVEL_WEIGHTS[logState.displayLevel] || 0;
      if (threshold <= 0) return text;
      var lines = String(text || '').split('\n');
      var kept = [];
      for (var i = 0; i < lines.length; i++) {
        var line = lines[i];
        if (line === '' || logLineWeight(line) >= threshold) kept.push(line);
      }
      return kept.join('\n');
    }

    function renderFromRaw() {
      var keepBottom = atBottom();
      var filtered = filterByLevel(logState.rawText);
      pre.innerHTML = filtered ? renderFriendlyLogHtml(filtered) : '';
      if (keepBottom) stickBottom();
    }

    function setRefreshButtons() {
      startButton.disabled = !!logState.refreshing;
      stopButton.disabled = !logState.refreshing;
      startButton.className = logState.refreshing ? 'cbi-button' : 'cbi-button cbi-button-apply';
      stopButton.className = logState.refreshing ? 'cbi-button cbi-button-apply' : 'cbi-button';
    }

    function buildLogUrl(lines, format, download) {
      return '/cgi-bin/luci/admin/services/smart_srun/log_tail?channel=plugin&lines=' + lines +
        '&format=' + encodeURIComponent(format || 'friendly') +
        (download ? '&download=1' : '') + '&_=' + Date.now();
    }

    function buildDownloadName() {
      var now = new Date();
      function pad(value) { return value < 10 ? '0' + value : String(value); }
      return 'smart_srun_plugin_' + now.getFullYear() +
        pad(now.getMonth() + 1) + pad(now.getDate()) + '_' +
        pad(now.getHours()) + pad(now.getMinutes()) + pad(now.getSeconds()) + '.log';
    }

    function refresh() {
      if (isPageHidden()) return;
      fetchJson(buildLogUrl(LOG_LIVE_LINES, 'friendly', false), function(err, data) {
        if (err || !data || typeof data.log !== 'string') return;
        logState.rawText = data.log;
        renderFromRaw();
      });
    }

    function startLoop() {
      if (logState.timer) return;
      logState.timer = setInterval(function() {
        if (logState.refreshing && !isPageHidden()) refresh();
      }, 2000);
    }

    function clearDisplay() {
      clearButton.disabled = true;
      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/log_clear', true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onreadystatechange = function() {
        if (xhr.readyState !== 4) return;
        clearButton.disabled = false;
        var data = {};
        try {
          data = JSON.parse(xhr.responseText || '{}');
        } catch (e) {}
        if (xhr.status === 200 && data.ok) {
          logState.rawText = '';
          pre.innerHTML = '';
        } else {
          alert(data.message || '清空失败');
        }
      };
      xhr.send('channel=plugin');
    }

    function triggerBlobDownload(text) {
      var urlApi = window.URL || window.webkitURL;
      if (!urlApi || !urlApi.createObjectURL) return;
      var blob = new Blob([text || ''], { type: 'text/plain;charset=utf-8' });
      var objUrl = urlApi.createObjectURL(blob);
      var link = document.createElement('a');
      link.href = objUrl;
      link.download = buildDownloadName();
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
      urlApi.revokeObjectURL(objUrl);
    }

    function downloadCurrentLog() {
      downloadButton.disabled = true;
      fetchJson(buildLogUrl(LOG_DOWNLOAD_LINES, 'raw', true), function(err, data) {
        downloadButton.disabled = false;
        if (err || !data || typeof data.log !== 'string') {
          alert('下载失败');
          return;
        }
        triggerBlobDownload(data.log);
      });
    }

    startButton.addEventListener('click', function() {
      if (logState.refreshing) return;
      logState.refreshing = true;
      setRefreshButtons();
      refresh();
    });

    stopButton.addEventListener('click', function() {
      if (!logState.refreshing) return;
      logState.refreshing = false;
      setRefreshButtons();
    });

    clearButton.addEventListener('click', clearDisplay);
    downloadButton.addEventListener('click', downloadCurrentLog);

    function applyDisplayLevel(rawValue) {
      var next = String(rawValue == null ? '' : rawValue).toUpperCase();
      if (!(next in LOG_LEVEL_WEIGHTS)) next = 'ALL';
      if (logState.displayLevel === next) return;
      logState.displayLevel = next;
      renderFromRaw();
    }

    if (levelFilter) {
      levelFilter.value = logState.displayLevel;
      levelFilter.addEventListener('change', function() {
        applyDisplayLevel(levelFilter.value);
      });
    }

    setRefreshButtons();
    if (logState.rawText) {
      renderFromRaw();
      stickBottom();
    }
    if (logState.refreshing) refresh();
    onPageVisible(function() {
      if (logState.refreshing) refresh();
    });
    startLoop();
  }

  function initAll() {
    initVersionNotice();
    initTables();
    initSchoolInfo();
    initOverview();
    initManualActions();
    initSwitchActions();
    initLogView();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAll);
  } else {
    initAll();
  }
})();
