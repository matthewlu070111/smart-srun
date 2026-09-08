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

  function overviewField(label, value, mono, unit) {
    var observed = value === undefined || value === null || value === '' ? '未知' : String(value) + (unit || '');
    return '<div class="smart-overview-field"><dt>' + escapeHtml(label) + '</dt><dd' +
      (mono ? ' class="smart-overview-mono"' : '') + '>' + escapeHtml(observed) + '</dd></div>';
  }

  function overviewGroup(label, fields) {
    return '<section class="smart-overview-group"><h5>' + escapeHtml(label) + '</h5><dl>' + fields + '</dl></section>';
  }

  function overviewPrimary(label, value) {
    return '<div class="smart-overview-primary"><span>' + escapeHtml(label) + '</span><strong>' + escapeHtml(value) + '</strong></div>';
  }

  function wirelessStatusMarkup(data) {
    if (data.current_campus_access_mode === 'wired' || data.mode_label === '校园网模式（有线）') return '';
    return overviewGroup('无线连接', overviewField('实际 AP', data.current_bssid, true) +
      overviewField('无线接口', data.current_wireless_ifname, true) +
      overviewField('信号', data.current_signal, true, ' dBm') +
      overviewField('信道', data.current_channel, true));
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
      '<div class="smart-native-row"><label>认证地址</label><input id="jm-base_url" value="' + escapeHtml(initialValues.base_url) + '"><div style="color:#6b7280;font-size:12px;margin-top:.25rem;">不知道填什么就留空，直接点下面的“自动探测”。</div></div>' +
      '<div class="smart-native-row"><label>AC_ID</label><span><input id="jm-ac_id" value="' + escapeHtml(initialValues.ac_id) + '"> <button type="button" id="jm-detect-acid" class="btn cbi-button cbi-button-action">自动探测</button> <span id="jm-detect-acid-status" style="margin-left:6px;color:#6b7280;"></span></span></div>' +
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
      var schoolDefaults = preset.defaults || {};
      var loginShape = preset.observed_login_shape || {};
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

    function fillField(node, value) {
      if (!node || !value) return false;
      node.value = value;
      node.dispatchEvent(new Event('change', { bubbles: true }));
      return true;
    }

    function applyDetectResult(data, nodes) {
      var acid = data.acid || data.ac_id || data.value || '';
      var filled = [];
      if (fillField(nodes.base, data.base_url || data.detected_url || '')) {
        filled.push('认证地址');
      }
      if (fillField(nodes.acid, acid)) filled.push('AC_ID ' + acid);
      var text = filled.length ? ('已填入 ' + filled.join('、'))
                               : (data.message || '未发现可用参数');
      if (nodes.status) nodes.status.textContent = text;
      else if (!filled.length) alert(text);
    }

    function detectAcidForForm() {
      var nodes = {
        base: document.getElementById('jm-base_url'),
        acid: document.getElementById('jm-ac_id'),
        status: document.getElementById('jm-detect-acid-status')
      };
      var button = document.getElementById('jm-detect-acid');
      var baseUrl = nodes.base ? nodes.base.value : '';
      // 地址空着不再拦人。填不出认证地址正是这个按钮要解决的问题，所以改成
      // 反过来问路由器：出口有没有被强制门户拦下来？拦下来的 302 跳转里
      // 就带着本校认证页地址。
      var path = baseUrl ? 'detect_acid' : 'detect_env';
      if (button) button.disabled = true;
      if (nodes.status) {
        nodes.status.textContent = baseUrl ? '嗅探中...' : '正在检查出口是否被认证页拦截...';
      }
      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/' + path, true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onload = function() {
        var data = {};
        try {
          data = JSON.parse(xhr.responseText || '{}');
        } catch (e) {}
        applyDetectResult(data, nodes);
        if (button) button.disabled = false;
      };
      xhr.onerror = function() {
        if (nodes.status) nodes.status.textContent = '嗅探请求失败';
        if (button) button.disabled = false;
      };
      xhr.send(baseUrl ? ('base_url=' + encodeURIComponent(baseUrl)) : '');
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

    var DOC_FALLBACK = 'https://smartsrun-doc.pages.dev/development/architecture';
    function docUrlFor(value) {
      var items = readJson('smart-auth-strategy-data', []);
      if (!Array.isArray(items)) items = [];
      for (var i = 0; i < items.length; i++) {
        if (items[i] && items[i].short_name === value && /^https?:\/\//i.test(items[i].doc_url || '')) return items[i].doc_url;
      }
      if (!value || value === 'default') return 'https://smartsrun-doc.pages.dev/guide/authentication';
      return DOC_FALLBACK;
    }
    var outerDescEl = null;
    for (var parent = infoBox.parentNode; parent; parent = parent.parentNode) {
      if (parent.className && String(parent.className).indexOf('cbi-value-description') >= 0) {
        outerDescEl = parent;
        break;
      }
    }

    function update(value) {
      infoBox.style.display = 'block';
      if (outerDescEl) outerDescEl.style.display = 'block';
      docLinkEl.href = docUrlFor(String(value || ''));
    }

    // The CBI widget may be created after this script initializes.
    var sel = document.querySelector('select[name="cbid.smart_srun.main.school"]');
    if (sel) update(sel.value);
    document.addEventListener('change', function(event) {
      var target = event.target;
      if (target && target.name === 'cbid.smart_srun.main.school') update(target.value);
    });
  }

  function initOverview() {
    var root = document.getElementById('smart-srun-overview');
    var title = document.getElementById('smart-srun-overview-title');
    var meta = document.getElementById('smart-srun-overview-meta');
    var details = document.getElementById('smart-srun-overview-details-content');
    var pendingLine = document.getElementById('smart-srun-overview-pending');
    if (!root || !title || !meta || !details || !pendingLine || window.__smartSrunOverviewInit) return;
    window.__smartSrunOverviewInit = true;

    var palette = {
      online: { light: '#237a4b', dark: '#79d6a5' },
      portal: { light: '#ad5b0a', dark: '#f4bd70' },
      limited: { light: '#b63232', dark: '#f59393' },
      offline: { light: '#626b78', dark: '#b5bdc9' }
    };

    function applyTone(level) {
      var tone = palette[level] || palette.offline;
      // Follow the surrounding LuCI theme, including themes switched without a reload.
      var color = window.getComputedStyle && root.parentNode ? window.getComputedStyle(root.parentNode).color.match(/[\d.]+/g) : null;
      var dark = color && (+color[0] * 0.299 + +color[1] * 0.587 + +color[2] * 0.114 > 160);
      root.style.color = dark ? '#e2e5eb' : '#303846';
      root.style.borderLeftColor = title.style.color = dark ? tone.dark : tone.light;
    }

    function refreshOverview() {
      fetchJson('/cgi-bin/luci/admin/services/smart_srun/status?_=' + Date.now(), function(err, data) {
        if (err) {
          applyTone('offline');
          if (title.textContent !== '状态读取失败') title.textContent = '状态读取失败';
          meta.textContent = '无法获取连接信息，请稍后重试。';
          details.textContent = '连接信息暂不可用。';
          pendingLine.textContent = ''; pendingLine.style.display = 'none';
          return;
        }
        var level = (typeof data.connectivity_level === 'string' && data.connectivity_level !== '') ? data.connectivity_level : 'offline';
        var status = (typeof data.status === 'string' && data.status !== '') ? data.status : '未知';
        var ssid = (typeof data.current_ssid === 'string' && data.current_ssid !== '') ? data.current_ssid : '未连接';
        var mode = (typeof data.mode_label === 'string' && data.mode_label !== '') ? data.mode_label : '未知模式';
        var conn = (typeof data.connectivity === 'string' && data.connectivity !== '') ? data.connectivity : '未知';
        var iface = (typeof data.current_iface === 'string' && data.current_iface !== '') ? data.current_iface : '--';
        var ip = (typeof data.current_ip === 'string' && data.current_ip !== '') ? data.current_ip : '--';
        var pending = (typeof data.pending_action === 'string' && data.pending_action !== '') ? ('待执行操作：' + data.pending_action) : '';
        var campusLabel = (typeof data.online_account_label === 'string' && data.online_account_label !== '') ? data.online_account_label : ((typeof data.campus_account_label === 'string' && data.campus_account_label !== '') ? data.campus_account_label : '--');
        var hotspotLabel = (typeof data.hotspot_profile_label === 'string' && data.hotspot_profile_label !== '') ? data.hotspot_profile_label : '--';

        applyTone(level);
        var interval = /^在线，下一次检测间隔 (\d+) 秒$/.exec(status);
        var displayStatus = interval ? '在线' : status;
        if (title.textContent !== displayStatus) title.textContent = displayStatus;
        pendingLine.textContent = pending; pendingLine.style.display = pending ? '' : 'none';
        var wired = data.current_campus_access_mode === 'wired' || mode === '校园网模式（有线）';
        var hotspot = data.mode === 'hotspot' || data.current_mode === 'hotspot' || mode === '热点模式';
        var metaHtml = overviewPrimary(wired ? '有线网络' : 'Wi-Fi', wired ? iface : ssid) +
          overviewPrimary(hotspot ? '热点配置' : '当前账号', hotspot ? hotspotLabel : campusLabel);
        if (meta.innerHTML !== metaHtml) meta.innerHTML = metaHtml;
        var runtime = overviewField('模式', mode) +
          overviewField(interval ? '检测间隔' : '运行状态', interval ? interval[1] + ' 秒' : status);
        if (!wired && !hotspot) {
          runtime += overviewField('AP 选择', apSelectionLabel(data.ap_selection_policy)) +
            overviewField('选择说明', data.ap_selection_reason);
        }
        var detailsHtml = overviewGroup('网络连接', overviewField('连通性', conn) +
          overviewField('网络接口', iface, true) + overviewField('IP 地址', ip, true)) +
          wirelessStatusMarkup(data) + overviewGroup('运行信息', runtime);
        if (details.innerHTML !== detailsHtml) details.innerHTML = detailsHtml;
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

  // 一键配置向导：所有提示保留在文档流中，避免主题把 alert-message 变成浮动通知。
  var WIZ_STEPS = ['网络接入', '认证地址', '认证后缀', '账号信息', '确认保存'];
  var WIZ_SHAPE_KEYS = ['n', 'type', 'enc', 'info_prefix', 'double_stack', 'login_os', 'login_name'];
  var WIZ_OUTCOME = {
    hit: '登录成功，已验证', online: '已在线，后缀待确认',
    credential: '账号或密码错误，后缀待确认', miss: '账号不存在', other: '验证未完成',
    limited: '认证过于频繁', identity: '在线账号后缀已确认', skipped: '未继续尝试'
  };
  var wiz = null;

  function wizEl(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = text;
    return n;
  }

  function wizStyles() {
    if (document.getElementById('smart-wizard-style')) return;
    var style = wizEl('style');
    style.id = 'smart-wizard-style';
    style.textContent =
      '.modal.smart-wizard-dialog{width:920px!important;max-width:calc(100vw - 24px)!important;' +
      'max-height:calc(100vh - 32px)!important;box-sizing:border-box;display:flex!important;flex-direction:column;' +
      'flex-wrap:nowrap!important;align-items:stretch!important;margin:16px auto!important;min-width:0!important;overflow:hidden;padding:20px!important;}' +
      '.smart-wizard-dialog>h4{flex:none;margin:0 0 16px!important;padding:0!important;box-shadow:none;}' +
      '.modal.smart-wizard-dialog>.smart-wizard{flex:1 1 auto;width:100%;align-self:stretch;margin:0;outline:none;}' +
      '.smart-wizard{display:flex;flex-direction:column;min-height:0;overflow:hidden;line-height:1.6;text-align:left;}' +
      '.smart-wizard *{box-sizing:border-box;}' +
      '.smart-wizard-steps{display:flex;flex:none;gap:8px;margin:0 0 16px;padding:0;list-style:none;}' +
      '.smart-wizard-steps li{flex:1;min-width:0;padding:8px 6px;border-bottom:3px solid rgba(127,127,127,.25);font-size:13px;}' +
      '.smart-wizard-steps li[aria-current=step]{border-color:#8674d9;font-weight:600;}' +
      '.smart-wizard-content{min-height:0;overflow:auto;padding:0 4px 6px;overscroll-behavior:contain;}' +
      '.smart-wizard-note{position:static!important;display:block!important;transform:none!important;' +
      'width:auto!important;margin:0 0 16px!important;padding:12px 14px;border:1px solid rgba(127,127,127,.25);' +
      'border-left:4px solid #7187bd;border-radius:6px;background:rgba(127,127,127,.08);overflow-wrap:anywhere;}' +
      '.smart-wizard-note-success{border-left-color:#4b9f76;}.smart-wizard-note-warning{border-left-color:#c99a35;}' +
      '.smart-wizard-note-danger{border-left-color:#d46a6a;}' +
      '.smart-wizard-row{display:grid;grid-template-columns:130px minmax(0,1fr);gap:6px 16px;margin:0 0 16px;align-items:start;}' +
      '.smart-wizard-row>label{padding-top:8px;text-align:right;}.smart-wizard-field{min-width:0;}' +
      '.smart-wizard-field>input,.smart-wizard-field>select{width:100%!important;max-width:100%!important;min-width:0!important;}' +
      '.smart-wizard-password{position:relative;}' +
      '.smart-wizard-password>input{width:100%!important;min-width:0!important;max-width:100%!important;padding-right:48px!important;}' +
      '.smart-wizard-password>.smart-wizard-password-toggle{position:absolute;right:1px;top:1px;bottom:1px;width:42px!important;min-width:42px!important;' +
      'padding:0!important;display:flex;align-items:center;justify-content:center;border:0!important;background:transparent!important;' +
      'box-shadow:none!important;color:inherit;opacity:.7;}' +
      '.smart-wizard-password-toggle:hover,.smart-wizard-password-toggle:focus{opacity:1;}' +
      '.smart-wizard-password-toggle:focus-visible{outline:2px solid #8674d9;outline-offset:-3px;}' +
      '.smart-wizard-password-toggle[aria-pressed=false] .smart-wizard-eye-slash{display:none;}' +
      '.smart-wizard-field>code{display:block;white-space:normal;overflow-wrap:anywhere;}' +
      '.smart-wizard input::placeholder{color:#888!important;opacity:1!important;font-weight:400!important;}' +
      '.smart-wizard-help{font-size:12px;opacity:.75;margin-top:5px;overflow-wrap:anywhere;}' +
      '.smart-wizard-actions{display:flex;flex-wrap:wrap;align-items:center;justify-content:flex-end;gap:8px;' +
      'flex:none;border-top:1px solid rgba(127,127,127,.25);margin-top:12px;padding-top:12px;}' +
      '.smart-wizard-actions .smart-wizard-help{flex:1 1 100%;margin:0 0 4px;}' +
      '.smart-wizard button{white-space:normal;height:auto!important;margin:0!important;}' +
      '.smart-wizard-actions .smart-wizard-close{margin-right:auto!important;}' +
      '.smart-wizard table{width:100%;table-layout:fixed;border-collapse:collapse;margin:8px 0 16px;}' +
      '.smart-wizard th,.smart-wizard td{text-align:left!important;padding:9px 8px!important;' +
      'overflow-wrap:anywhere;border-bottom:1px solid rgba(127,127,127,.2);font-size:13px;}' +
      '.smart-wizard td label{cursor:pointer;display:block;}.smart-wizard td input{margin-right:6px;}' +
      '.smart-wizard-summary td:first-child{width:30%;opacity:.75;}' +
      '.smart-wizard-inline{display:flex;flex-wrap:wrap;gap:8px;}.smart-wizard-inline input{flex:1;min-width:140px;}' +
      '.smart-wizard-log{white-space:pre-wrap;overflow-wrap:anywhere;max-height:150px;overflow:auto;font-size:12px;}' +
      '@media(max-width:600px){.modal.smart-wizard-dialog{padding:14px!important;max-height:calc(100vh - 16px)!important;}' +
      '.smart-wizard-steps{gap:3px;}.smart-wizard-steps li{font-size:11px;padding:6px 2px;}' +
      '.smart-wizard-step-label{display:block;word-break:keep-all;}' +
      '.smart-wizard-row{grid-template-columns:minmax(0,1fr);gap:4px;margin-bottom:14px;}' +
      '.smart-wizard-row>label{text-align:left;padding-top:0;font-weight:600;}' +
      '.smart-wizard th,.smart-wizard td{padding:8px 4px!important;font-size:12px;}}';
    document.head.appendChild(style);
  }

  function wizReset() {
    if (wiz && wiz.xhr) wiz.xhr.abort();
    wiz = {
      step: 0, env: null, busy: '', error: '', addressHint: '',
      baseUrl: '', portalUrl: '', acId: '', school: '', shape: {}, userId: '', password: '',
      accessMode: '', wiredIface: 'wan', wifiIface: '', ssid: '',
      portalOps: [], portalOpsHint: '', portalOpsKey: '',
      wifiKey: '', wifiEncryption: 'auto', wifiRadio: '', wifiJob: '', wifiState: '', wifiMessage: '', wifiTimer: null,
      ops: [], selectedSuffix: null, opConfirmed: false, opLog: '', verifyExpanded: false, xhr: null
    };
  }

  function wizInvalidate() {
    wiz.opConfirmed = false;
    wiz.passwordVerified = false;
    wiz.opLog = '';
    for (var i = 0; i < wiz.ops.length; i++) { wiz.ops[i].outcome = ''; wiz.ops[i].message = ''; }
    if (wiz.root) {
      var result = wiz.root.querySelector('#wiz-account-result');
      if (result) result.parentNode.removeChild(result);
    }
  }

  function wizRow(title, field, descr) {
    var row = wizEl('div', 'smart-wizard-row');
    var label = wizEl('label', '', title);
    if (field.id) label.htmlFor = field.id;
    row.appendChild(label);
    var value = wizEl('div', 'smart-wizard-field');
    if (field.type === 'password') {
      var wrap = wizEl('div', 'smart-wizard-password');
      var toggle = wizEl('button', 'btn cbi-button smart-wizard-password-toggle');
      toggle.type = 'button';
      toggle.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" ' +
        'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">' +
        '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z"/>' +
        '<circle cx="12" cy="12" r="3"/><path class="smart-wizard-eye-slash" d="m3 3 18 18"/></svg>';
      toggle.title = '显示' + title;
      toggle.setAttribute('aria-controls', field.id);
      toggle.setAttribute('aria-pressed', 'false');
      toggle.setAttribute('aria-label', '显示' + title);
      toggle.onclick = function() {
        var visible = field.type === 'password';
        field.type = visible ? 'text' : 'password';
        toggle.title = (visible ? '隐藏' : '显示') + title;
        toggle.setAttribute('aria-pressed', visible ? 'true' : 'false');
        toggle.setAttribute('aria-label', (visible ? '隐藏' : '显示') + title);
      };
      wrap.appendChild(field); wrap.appendChild(toggle); value.appendChild(wrap);
    } else value.appendChild(field);
    if (descr) value.appendChild(wizEl('div', 'smart-wizard-help', descr));
    row.appendChild(value);
    return row;
  }

  function wizInput(id, key, type, placeholder) {
    var input = wizEl('input');
    input.id = id;
    input.type = type || 'text';
    input.value = wiz[key] || '';
    if (placeholder) input.placeholder = placeholder;
    input.oninput = function() {
      wiz[key] = (key === 'password' || key === 'wifiKey' || key === 'ssid') ? input.value : input.value.replace(/^\s+|\s+$/g, '');
      wizInvalidate();
      if (key === 'baseUrl') wiz.portalUrl = '';
      if (key === 'wiredIface' || key === 'wifiIface' || key === 'ssid') {
        wiz.env = null;
        var oldResult = document.getElementById('wiz-env-result');
        if (oldResult) oldResult.textContent = '接入信息已更改，请重新检测所选线路。';
        var oldDetails = document.getElementById('wiz-env-details');
        if (oldDetails) oldDetails.parentNode.removeChild(oldDetails);
      }
      if (key === 'userId') wizUpdatePreviews();
    };
    return input;
  }

  function wizAlert(kind, text, headline) {
    var box = wizEl('div', 'smart-wizard-note smart-wizard-note-' + kind);
    box.setAttribute('role', kind === 'danger' ? 'alert' : 'status');
    if (headline) box.appendChild(wizEl('strong', '', headline));
    box.appendChild(wizEl('div', '', text));
    return box;
  }

  function wizError(text) {
    wiz.nextAfterWifi = false;
    wiz.error = text;
    wizRender();
    var box = document.getElementById('wiz-error');
    if (box) { box.tabIndex = -1; box.focus(); }
  }

  function wizGo(step) {
    wiz.error = '';
    wiz.step = step;
    wizRender();
    if (step === 2) wizDiscoverOperators(false);
  }

  function wizClose() {
    if (wiz && (wiz.busy === 'operator' || wiz.busy === 'save')) return;
    var old = wiz;
    if (old && old.wifiTimer) clearTimeout(old.wifiTimer);
    if (old && old.wifiJob) {
      var cancel = new XMLHttpRequest();
      cancel.open('POST', '/cgi-bin/luci/admin/services/smart_srun/setup_wifi', true);
      cancel.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded');
      cancel.send('action=cancel&job=' + encodeURIComponent(old.wifiJob));
    }
    wiz = null;
    if (old && old.xhr) old.xhr.abort();
    L.hideModal();
    var trigger = document.getElementById('smart-srun-setup-wizard');
    if (trigger) trigger.focus();
    if (old && old.saved) location.reload();
  }

  function wizActions(note, buttons) {
    var bar = wizEl('div', 'smart-wizard-actions');
    if (note) bar.appendChild(wizEl('div', 'smart-wizard-help', note));
    var close = wizEl('button', 'btn cbi-button smart-wizard-close', wiz.saveComplete ? '完成' :
      (wiz.wifiJob && wiz.wifiState !== 'connected' ? '关闭并恢复原网络' : '关闭'));
    close.type = 'button';
    close.onclick = wizClose;
    close.disabled = wiz.busy === 'operator' || wiz.busy === 'save';
    bar.appendChild(close);
    for (var i = 0; i < buttons.length; i++) {
      var b = buttons[i];
      if (b.hidden) continue;
      var button = wizEl('button', 'btn cbi-button ' + (b.cls || ''), b.label);
      button.type = 'button';
      button.disabled = !!wiz.busy || !!b.disabled;
      button.onclick = b.onclick;
      bar.appendChild(button);
    }
    return bar;
  }

  function wizPost(path, values, done, timeout) {
    var owner = wiz;
    var xhr = new XMLHttpRequest();
    owner.xhr = xhr;
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/' + path, true);
    xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
    xhr.timeout = timeout || 120000;
    var encoded = [];
    for (var key in values) {
      if (Object.prototype.hasOwnProperty.call(values, key)) encoded.push(encodeURIComponent(key) + '=' + encodeURIComponent(values[key]));
    }
    function finish(err, data) {
      if (wiz !== owner || owner.xhr !== xhr) return;
      owner.xhr = null;
      owner.busy = '';
      // Escape or another LuCI modal may have hidden/replaced this wizard.
      if (!owner.root || !document.body.contains(owner.root) ||
          !document.body.classList.contains('modal-overlay-active')) return;
      done(err, data || {});
    }
    xhr.onload = function() {
      if (xhr.status < 200 || xhr.status >= 300) { finish(new Error('请求失败（HTTP ' + xhr.status + '），请检查 LuCI 登录状态。')); return; }
      var data;
      try { data = JSON.parse(xhr.responseText); } catch (e) { finish(new Error('未收到有效结果，请检查 LuCI 登录是否过期。')); return; }
      if (!data || typeof data !== 'object' || Array.isArray(data)) { finish(new Error('探测结果格式错误')); return; }
      finish(null, data);
    };
    xhr.onerror = function() { finish(new Error('无法连接路由器，请检查连接后重试。')); };
    xhr.ontimeout = function() { finish(new Error(path === 'enqueue' ?
      '保存请求超时，账号可能已保存。请关闭并刷新账号列表核对后再操作。' :
      '请求超时。路由器可能仍在处理，请稍后再试。')); };
    xhr.send(encoded.join('&'));
  }

  function wizConnection() {
    return { access_mode: wiz.accessMode, iface: wiz.accessMode === 'wired' ? wiz.wiredIface : wiz.wifiIface, ssid: wiz.ssid };
  }

  function wizLoginPreview(suffix) {
    var user = wiz.userId || '账号';
    return suffix ? user + '@' + suffix : user;
  }

  function wizUpdatePreviews() {
    var preview = document.getElementById('wiz-login-preview');
    if (preview) preview.textContent = wiz.selectedSuffix === null ? '待选择认证后缀' : wizLoginPreview(wiz.selectedSuffix);
  }

  function wizPresetSelect() {
    var select = wizEl('select');
    select.id = 'wiz-preset';
    var presets = [{ short_name: '', name: '无预设' }].concat(schoolPresetList(), loadCustomPresets());
    for (var i = 0; i < presets.length; i++) {
      var option = wizEl('option', '', presets[i].name || presets[i].short_name);
      option.value = presets[i].short_name || '';
      option.selected = option.value === wiz.school;
      select.appendChild(option);
    }
    select.onchange = function() {
      wiz.school = select.value;
      var preset = findSchoolPreset(wiz.school) || {};
      var defaults = preset.defaults || {};
      wiz.baseUrl = defaults.base_url || '';
      wiz.portalUrl = '';
      wiz.acId = defaults.ac_id || '';
      if (!wiz.wifiJob) wiz.ssid = defaults.ssid || '';
      wiz.shape = {};
      WIZ_SHAPE_KEYS.forEach(function(key) {
        var observed = preset.observed_login_shape || {};
        var observedKey = key === 'login_os' ? 'os' : (key === 'login_name' ? 'name' : key);
        if (defaults[key] !== undefined) wiz.shape[key] = defaults[key];
        else if (observed[observedKey] !== undefined) wiz.shape[key] = observed[observedKey];
      });
      wiz.ops = [];
      wiz.portalOps = []; wiz.portalOpsHint = ''; wiz.portalOpsKey = '';
      wiz.selectedSuffix = null;
      wiz.env = null;
      wizInvalidate();
      wiz.addressHint = '';
      wizRender();
    };
    return select;
  }

  function wizDetectEnvironment() {
    if (!wiz.accessMode) { wizError('请选择网络接入方式。'); return; }
    if (wiz.accessMode === 'wired' && !wiz.wiredIface) { wizError('请填写有线出口接口。'); return; }
    wiz.busy = 'environment';
    wiz.error = '';
    var payload = wizConnection();
    payload.base_url = wiz.baseUrl;
    payload.school = wiz.school;
    wizRender();
    wizPost('detect_env', payload, function(err, data) {
      if (err) { wizError(err.message); return; }
      if (!data.state) { wizError(data.message || '路由器未返回出口状态'); return; }
      wiz.env = data;
      if (data.ok && data.base_url) wiz.baseUrl = data.base_url;
      if (data.ok) wiz.portalUrl = data.portal_url || '';
      if (data.acid) wiz.acId = data.acid;
      if (wiz.accessMode === 'wifi' && data.iface) wiz.wifiIface = data.iface;
      if (wiz.accessMode === 'wifi' && data.ssid && !wiz.ssid) wiz.ssid = data.ssid;
      wiz.addressHint = data.message || '';
      if (wiz.nextAfterWifi) { wiz.nextAfterWifi = false; wizGo(1); return; }
      wizRender();
    });
  }

  function wizPollWifi() {
    var owner = wiz;
    if (!owner || !owner.wifiJob) return;
    owner.busy = 'wifi';
    wizPost('setup_wifi', {action: 'status', job: owner.wifiJob}, function(err, data) {
      if (wiz !== owner) return;
      if (err || data.state === 'missing') {
        if (Date.now() > owner.wifiDeadline) { wizError('暂时无法联系路由器。请恢复连接后重新打开向导；临时网络会在配置超时后恢复。'); return; }
        owner.wifiMessage = '等待路由器连接恢复…';
      } else {
        owner.wifiState = data.state || 'failed'; owner.wifiMessage = data.message || '';
        if (data.state === 'ready' || data.state === 'connected') {
          owner.wifiIface = data.iface; owner.wifiRadio = data.radio || ''; owner.wifiKey = '';
          if (data.state === 'ready') owner.wifiTimer = setTimeout(wizWatchWifi, 10000);
          wizDetectEnvironment(); return;
        }
        if (data.ok === false || data.state === 'done' || data.state === 'cancelled') {
          owner.wifiJob = ''; wizError(data.message || '无线连接未完成，请重新连接。'); return;
        }
      }
      owner.busy = 'wifi'; wizRender();
      owner.wifiTimer = setTimeout(wizPollWifi, 2000);
    }, 10000);
  }

  function wizWatchWifi() {
    if (!wiz || !wiz.wifiJob || wiz.wifiState !== 'ready') return;
    if (wiz.busy || wiz.xhr) { wiz.wifiTimer = setTimeout(wizWatchWifi, 10000); return; }
    wizPost('setup_wifi', {action: 'status', job: wiz.wifiJob}, function(err, data) {
      if (!err && data.state !== 'ready') {
        wiz.wifiJob = ''; wiz.env = null;
        wizError(data.message || '无线连接已结束，请返回第一步重新连接。'); return;
      }
      wiz.wifiTimer = setTimeout(wizWatchWifi, 10000);
    }, 5000);
  }

  function wizChangeWifi() {
    var owner = wiz;
    if (owner.wifiTimer) clearTimeout(owner.wifiTimer);
    owner.busy = 'wifi'; owner.error = ''; wizRender();
    function check() {
      wizPost('setup_wifi', {action: 'status', job: owner.wifiJob}, function(err, data) {
        if (!err && ['failed', 'cancelled', 'connected', 'done'].indexOf(data.state) !== -1) {
          owner.wifiJob = ''; owner.wifiState = ''; owner.wifiMessage = ''; owner.env = null;
          owner.wifiIface = ''; owner.wifiRadio = ''; wizRender(); return;
        }
        owner.busy = 'wifi'; owner.wifiTimer = setTimeout(check, 2000);
      }, 10000);
    }
    wizPost('setup_wifi', {action: 'cancel', job: owner.wifiJob}, function() { check(); }, 10000);
  }

  function wizConnectWifi() {
    if (wiz.accessMode !== 'wifi') { wizDetectEnvironment(); return; }
    if (!wiz.ssid) { wizError('请填写校园网 Wi-Fi 名称，或先选择学校预设。'); return; }
    if (wiz.wifiJob) { wizDetectEnvironment(); return; }
    var id = '';
    for (var i = 0; i < 32; i++) id += Math.floor(Math.random() * 16).toString(16);
    wiz.wifiJob = id; wiz.wifiState = 'starting'; wiz.busy = 'wifi'; wiz.error = '';
    wiz.wifiDeadline = Date.now() + 300000;
    wiz.wifiMessage = '正在查找校园网 Wi-Fi…';
    var payload = {action: 'start', job: id, ssid: wiz.ssid,
      key: wiz.wifiEncryption === 'none' ? '' : wiz.wifiKey, encryption: wiz.wifiEncryption,
      iface: wiz.wifiIface, radio: wiz.wifiRadio};
    wizRender();
    wizPost('setup_wifi', payload, function(err, data) {
      if (!err && data.ok === false) { wiz.wifiJob = ''; wizError(data.message || '无法启动连接'); return; }
      wizPollWifi();
    }, 10000);
  }

  function wizStepEnv(body) {
    var mode = wizEl('select');
    mode.id = 'wiz-mode';
    [['', '请选择接入方式'], ['wired', '有线网络'], ['wifi', '无线网络（Wi-Fi）']].forEach(function(pair) {
      var option = wizEl('option', '', pair[1]);
      option.value = pair[0]; option.selected = pair[0] === wiz.accessMode;
      mode.appendChild(option);
    });
    mode.onchange = function() { wiz.accessMode = mode.value; wiz.env = null; wizInvalidate(); wizRender(); };
    body.appendChild(wizRow('接入方式', mode));
    if (wiz.accessMode) body.appendChild(wizRow('学校预设（可选）', wizPresetSelect()));
    if (wiz.accessMode === 'wired') {
      body.appendChild(wizRow('有线出口接口', wizInput('wiz-iface', 'wiredIface', 'text', 'wan'), '校园网对应的网络接口，可在“网络 → 接口”中查看。'));
    } else if (wiz.accessMode === 'wifi') {
      body.appendChild(wizRow('校园网 Wi-Fi 名称', wizInput('wiz-ssid', 'ssid', 'text', '例如 CampusWiFi')));
      var encryption = wizEl('select'); encryption.id = 'wiz-wifi-encryption';
      [['auto', '自动识别'], ['none', '开放网络'], ['psk2', 'WPA2-PSK'], ['sae', 'WPA3-SAE'],
        ['sae-mixed', 'WPA2/WPA3 混合'], ['psk-mixed', 'WPA/WPA2 混合'], ['psk', 'WPA-PSK']].forEach(function(pair) {
        var option = wizEl('option', '', pair[1]); option.value = pair[0];
        option.selected = pair[0] === wiz.wifiEncryption; encryption.appendChild(option);
      });
      encryption.onchange = function() {
        wiz.wifiEncryption = encryption.value; wiz.env = null;
        if (wiz.wifiEncryption === 'none') wiz.wifiKey = '';
        wizInvalidate(); wizRender();
      };
      body.appendChild(wizRow('加密方式', encryption));
      if (wiz.wifiEncryption !== 'none') {
        body.appendChild(wizRow('Wi-Fi 密码', wizInput('wiz-wifi-key', 'wifiKey', 'password',
          wiz.wifiEncryption === 'auto' ? '加密网络需要填写' : '请输入 Wi-Fi 密码')));
      }
      var advanced = wizEl('details'); advanced.appendChild(wizEl('summary', '', '高级网络设置'));
      advanced.appendChild(wizRow('无线出口接口', wizInput('wiz-wifi-iface', 'wifiIface', 'text', '自动选择或创建 wwan'), '多线路时可指定网络接口名，不能填写 LAN 接口。'));
      advanced.appendChild(wizRow('无线电', wizInput('wiz-wifi-radio', 'wifiRadio', 'text', '自动选择可用无线电'), '需要指定频段时填写 radio0、radio1 等无线电名称。'));
      body.appendChild(advanced);
    }
    if (wiz.wifiMessage) body.appendChild(wizAlert('info', wiz.wifiMessage));
    if (wiz.busy && wiz.busy !== 'wifi') body.appendChild(wizAlert('info', '正在检测网络与认证地址…'));
    else if (wiz.env) {
      var e = wiz.env;
      var result = wizAlert(e.ok ? 'success' : (e.state === 'down' ? 'warning' : 'info'), e.message || '',
        e.ok ? '已找到认证地址' : (e.state === 'online' ? '网络已连接' : '检测完成'));
      result.id = 'wiz-env-result';
      if (e.ok && e.base_url) result.appendChild(wizEl('div', '', e.base_url + ' · AC_ID：' + (e.acid || '待确认')));
      body.appendChild(result);
      var details = wizEl('details');
      details.id = 'wiz-env-details';
      details.appendChild(wizEl('summary', '', '查看检测详情'));
      var table = wizSummary([
        ['检测出口', e.iface || wizConnection().iface || '自动匹配'],
        ['外网状态', {online: '可访问外网', portal: '发现网页拦截', down: '暂不可达'}[e.state] || '未知'],
        ['HTTP 状态码', e.status_code || '—'], ['认证地址', e.base_url || '暂未发现'],
        ['AC_ID', e.acid || '暂未发现'], ['地址来源', e.address_source || (e.base_url ? '门户跳转' : '—')]
      ]);
      details.appendChild(table); body.appendChild(details);
    }
    body.appendChild(wizActions('', [
      { label: wiz.busy ? '检测中…' : (wiz.accessMode === 'wifi' && !wiz.wifiJob ? '连接并检测' : (wiz.env ? '重新检测' : '检测所选线路')), cls: 'cbi-button-neutral', disabled: !wiz.accessMode, onclick: wizConnectWifi },
      { label: '更换 Wi-Fi', hidden: !wiz.wifiJob, onclick: wizChangeWifi },
      { label: '下一步', cls: 'cbi-button-action', disabled: !wiz.accessMode, onclick: function() {
        if (wiz.accessMode === 'wired' && !wiz.wiredIface) { wizError('请填写有线出口接口。'); return; }
        if (wiz.accessMode === 'wifi' && !wiz.wifiJob) { wiz.nextAfterWifi = true; wizConnectWifi(); return; }
        wizGo(1);
      } }
    ]));
  }

  function wizNormalizeAddress() {
    var raw = wiz.baseUrl;
    if (!/^https?:\/\//i.test(raw)) raw = 'http://' + raw;
    var parser = document.createElement('a'); parser.href = raw;
    if (!wiz.baseUrl || !/^https?:$/.test(parser.protocol) || !parser.hostname || /\s|@/.test(parser.host) ||
        /^(?!https?:)[a-z][a-z0-9+.-]*:/i.test(wiz.baseUrl) || /\s/.test(wiz.baseUrl) || /https?:\/\/[^/]*@/i.test(raw)) return false;
    var acid = /[?&](?:ac_id|acid)=([^&#]+)/i.exec(parser.search);
    if (acid) { try { wiz.acId = decodeURIComponent(acid[1]); } catch (e) { return false; } }
    if (wiz.acId && !/^[A-Za-z0-9_.-]+$/.test(wiz.acId)) return false;
    var origin = parser.protocol + '//' + parser.host;
    if ((parser.pathname && parser.pathname !== '/') || parser.search) wiz.portalUrl = raw;
    var portal = document.createElement('a'); portal.href = wiz.portalUrl || origin;
    if (portal.protocol + '//' + portal.host !== origin) wiz.portalUrl = '';
    wiz.baseUrl = origin;
    return true;
  }

  function wizStepAddress(body) {
    body.appendChild(wizRow('学校预设', wizPresetSelect()));
    body.appendChild(wizRow('认证地址', wizInput('wiz-base', 'baseUrl', 'text', '校园网登录页地址')));
    body.appendChild(wizRow('AC_ID', wizInput('wiz-acid', 'acId', 'text', '自动检测'), '未检测到时可手动填写；留空保存使用默认值 1。'));
    if (wiz.addressHint) body.appendChild(wizAlert('info', wiz.addressHint));
    body.appendChild(wizActions('', [
      { label: '上一步', onclick: function() { wizGo(0); } },
      { label: wiz.busy ? '检测中…' : '检测认证参数', cls: 'cbi-button-neutral', onclick: function() {
        if (!wiz.baseUrl) { wizDetectEnvironment(); return; }
        var payload = wizConnection(); payload.base_url = wiz.baseUrl;
        wiz.busy = 'address'; wiz.error = ''; wizRender();
        wizPost('detect_acid', payload, function(err, data) {
          if (err) { wizError(err.message); return; }
          if (data.ok && data.base_url) wiz.baseUrl = data.base_url;
          if (data.ok) wiz.portalUrl = data.detected_url || payload.base_url;
          if (data.acid) wiz.acId = data.acid;
          wiz.addressHint = data.message || '未发现新的参数，请检查地址或手动填写。';
          wizRender();
        });
      } },
      { label: '下一步', cls: 'cbi-button-action', onclick: function() {
        if (!wizNormalizeAddress()) { wizError('请填写有效的 HTTP / HTTPS 认证地址和 AC_ID，或选择学校预设。'); return; }
        wizGo(2);
      } }
    ]));
  }

  function wizBuildCandidates() {
    var out = [], seen = {};
    function push(suffix, label, source) {
      if (typeof suffix !== 'string') return;
      var value = suffix.replace(/^\s+|\s+$/g, '');
      if (value === '??') return;
      if (Object.prototype.hasOwnProperty.call(seen, value)) return;
      seen[value] = true;
      out.push({ suffix: value, label: label || value || '不加后缀', src: source, outcome: '' });
    }
    if (wiz.portalOps.length) {
      wiz.portalOps.forEach(function(op) { push(op.suffix, op.label, '认证页面'); });
      return out;
    }
    var ops = (findSchoolPreset(wiz.school) || {}).operators || [];
    ops.forEach(function(op) { push(op.suffix !== undefined ? op.suffix : op.id, op.label, '学校预设'); });
    return out;
  }

  function wizSetCandidates(ops) {
    wiz.ops = ops;
    if (!ops.some(function(op) { return op.suffix === wiz.selectedSuffix; })) {
      wiz.selectedSuffix = ops.length === 1 ? ops[0].suffix : null;
    }
    wizInvalidate();
  }

  function wizDiscoverOperators(force) {
    var payload = wizConnection(); payload.base_url = wiz.portalUrl || wiz.baseUrl; payload.ac_id = wiz.acId;
    var key = JSON.stringify(payload);
    if (wiz.busy || (!force && wiz.portalOpsKey === key)) return;
    var manual = wiz.portalOpsKey === key ? wiz.ops.filter(function(op) { return !!op.manual; }) : [];
    var selected = wiz.portalOpsKey === key ? wiz.selectedSuffix : null;
    wiz.selectedSuffix = selected;
    wiz.portalOpsKey = key; wiz.portalOps = [];
    wizSetCandidates(wizBuildCandidates());
    wiz.busy = 'discovery'; wiz.portalOpsHint = '正在读取认证后缀…';
    wizRender();
    wizPost('discover_operators', payload, function(err, data) {
      wiz.portalOps = data.ok && Array.isArray(data.operators) ? data.operators : [];
      wiz.portalOpsHint = err ? err.message : (wiz.portalOps.length ? '已读取 ' + wiz.portalOps.length + ' 个认证后缀。' : (data.message || '未识别到认证后缀。'));
      var ops = wizBuildCandidates();
      if (!wiz.portalOps.length && ops.length) wiz.portalOpsHint = '已载入学校预设：' + ops.length + ' 个认证后缀。';
      manual.forEach(function(op) { if (!ops.some(function(item) { return item.suffix === op.suffix; })) ops.push(op); });
      wiz.selectedSuffix = selected; wizSetCandidates(ops); wizRender();
    }, 25000);
  }

  function wizStepOperator(body) {
    if (!wiz.ops.length && !wiz.busy) wizSetCandidates(wizBuildCandidates());
    if (wiz.portalOpsHint) body.appendChild(wizAlert(wiz.portalOps.length ? 'success' : 'info', wiz.portalOpsHint));
    var table = wizEl('table');
    var head = wizEl('tr');
    ['账号类型', '认证后缀'].forEach(function(text) { head.appendChild(wizEl('th', '', text)); });
    table.appendChild(head);
    wiz.ops.forEach(function(op) {
      var row = wizEl('tr');
      var cell = wizEl('td');
      var label = wizEl('label');
      var radio = wizEl('input'); radio.type = 'radio'; radio.name = 'wiz-operator';
      radio.value = op.suffix; radio.checked = wiz.selectedSuffix === op.suffix;
      radio.onchange = function() { wiz.selectedSuffix = op.suffix; wizInvalidate(); wizRender(); };
      label.appendChild(radio); label.appendChild(document.createTextNode(op.label));
      cell.appendChild(label); cell.appendChild(wizEl('div', 'smart-wizard-help', op.src)); row.appendChild(cell);
      row.appendChild(wizEl('td', '', op.suffix ? '@' + op.suffix : '不加后缀'));
      table.appendChild(row);
    });
    if (wiz.ops.length) body.appendChild(table);
    else if (!wiz.busy) body.appendChild(wizEl('p', 'smart-wizard-help', '请选择学校预设或填写认证后缀；无后缀账号请选择“不加后缀”。'));
    var custom = wizEl('input'); custom.id = 'wiz-op-add'; custom.placeholder = '例如 stu.example.edu.cn'; custom.type = 'text';
    var add = wizEl('button', 'btn cbi-button', '添加并选择'); add.type = 'button';
    add.onclick = function() {
      var value = custom.value.replace(/^[@\s]+|\s+$/g, '');
      if (!value || /[\s,@]/.test(value) || value === '??') { wizError('请填写实际后缀；无需后缀时选择“不加后缀”。'); return; }
      var exists = wiz.ops.some(function(op) { return op.suffix === value; });
      if (!exists) wiz.ops.push({suffix: value, label: value, src: '手动填写', manual: true, outcome: ''});
      wiz.selectedSuffix = value; wizInvalidate(); wizRender();
    };
    var wrap = wizEl('div', 'smart-wizard-inline'); wrap.appendChild(custom); wrap.appendChild(add);
    var plain = wizEl('button', 'btn cbi-button', '不加后缀'); plain.type = 'button';
    plain.onclick = function() {
      if (!wiz.ops.some(function(op) { return op.suffix === ''; })) {
        wiz.ops.push({suffix: '', label: '不加后缀', src: '手动选择', manual: true, outcome: ''});
      }
      wiz.selectedSuffix = ''; wizInvalidate(); wizRender();
    };
    wrap.appendChild(plain);
    body.appendChild(wizRow('自定义后缀', wrap));
    body.appendChild(wizActions('', [
      { label: '上一步', onclick: function() { wizGo(1); } },
      { label: '重新读取认证页', cls: 'cbi-button-neutral', onclick: function() { wizDiscoverOperators(true); } },
      { label: '下一步', cls: 'cbi-button-action', onclick: function() { wizGo(3); } }
    ]));
  }

  function wizSelectedOperator() {
    for (var i = 0; i < wiz.ops.length; i++) {
      if (wiz.ops[i].suffix === wiz.selectedSuffix) return wiz.ops[i];
    }
    return {label: '尚未选择', suffix: '', src: ''};
  }

  function wizStepAccount(body) {
    var select = wizEl('select'); select.id = 'wiz-account-operator';
    var pending = wizEl('option', '', '请选择账号类型'); pending.value = ''; select.appendChild(pending);
    var choices = wiz.ops.slice();
    if (!choices.some(function(op) { return op.suffix === ''; })) {
      choices.push({suffix: '', label: '不加后缀', src: '手动选择', manual: true, outcome: ''});
    }
    choices.forEach(function(op, index) {
      var option = wizEl('option', '', op.label + (op.suffix ? '（@' + op.suffix + '）' : ''));
      // Use an index so the empty suffix remains distinct from "not selected".
      option.value = String(index); select.appendChild(option);
      if (op.suffix === wiz.selectedSuffix) select.value = option.value;
    });
    select.onchange = function() {
      var chosen = select.value === '' ? null : choices[Number(select.value)];
      wiz.selectedSuffix = chosen ? chosen.suffix : null;
      if (chosen && !wiz.ops.some(function(op) { return op.suffix === chosen.suffix; })) wiz.ops.push(chosen);
      wizInvalidate(); wiz.error = '';
      var error = document.getElementById('wiz-error');
      if (error) error.parentNode.removeChild(error);
      wizUpdatePreviews();
    };
    body.appendChild(wizRow('账号类型', select));
    body.appendChild(wizRow('校园网账号', wizInput('wiz-user', 'userId', 'text', '学工号或校园网用户名'), '账号已包含后缀时，请选择“不加后缀”。'));
    body.appendChild(wizRow('密码', wizInput('wiz-pass', 'password', 'password')));
    var preview = wizEl('code', '', wiz.selectedSuffix === null ? '待选择认证后缀' : wizLoginPreview(wiz.selectedSuffix));
    preview.id = 'wiz-login-preview';
    body.appendChild(wizRow('登录名预览', preview));

    var verify = wizEl('details'); verify.id = 'wiz-account-verify';
    verify.open = !!wiz.verifyExpanded;
    verify.appendChild(wizEl('summary', '', '账号验证（可选）'));
    verify.appendChild(wizEl('p', 'smart-wizard-help', '在线账号识别无需密码。登录验证需提交账号和密码，最多验证 5 个已知后缀。'));
    var controls = wizEl('div', 'smart-wizard-inline');
    var identify = wizEl('button', 'btn cbi-button', '识别在线账号'); identify.type = 'button';
    identify.onclick = function() { wizRunOperatorProbe(true); }; controls.appendChild(identify);
    var login = wizEl('button', 'btn cbi-button', '验证登录'); login.type = 'button';
    login.onclick = function() { wizRunOperatorProbe(false); }; controls.appendChild(login);
    verify.appendChild(controls);
    if (wiz.opLog) {
      var result = wizEl('div'); result.id = 'wiz-account-result';
      if (wiz.opConfirmed) result.appendChild(wizAlert('success', wiz.passwordVerified ?
        '账号登录已验证。' : '已从在线账号确认后缀；本次填写的密码尚未验证。'));
      var attempts = wiz.ops.filter(function(op) { return !!op.outcome; });
      if (attempts.length) {
        var table = wizEl('table');
        attempts.forEach(function(op) {
          var row = wizEl('tr'); row.appendChild(wizEl('td', '', wizLoginPreview(op.suffix)));
          var outcome = wizEl('td', 'smart-wizard-outcome', WIZ_OUTCOME[op.outcome] || '验证未完成');
          if (op.message) outcome.appendChild(wizEl('div', 'smart-wizard-help', op.message));
          row.appendChild(outcome); table.appendChild(row);
        });
        result.appendChild(table);
      }
      result.appendChild(wizEl('div', 'smart-wizard-log', wiz.opLog)); verify.appendChild(result);
    }
    body.appendChild(verify);
    body.appendChild(wizActions('', [
      { label: '上一步', onclick: function() { wizGo(2); } },
      { label: '下一步', cls: 'cbi-button-action', onclick: function() {
        if (!wiz.userId || !wiz.password) { wizError('请填写校园网账号和密码。'); return; }
        if (wiz.selectedSuffix === null) { wizError('请选择账号类型或识别在线账号。'); return; }
        wizGo(4);
      } }
    ]));
  }

  function wizRunOperatorProbe(readOnly) {
    if (!wiz.userId) { wizError('请填写校园网账号。'); return; }
    if (!readOnly && !wiz.password) { wizError('验证登录需要填写密码；已有在线账号可使用“识别在线账号”。'); return; }
    var suffixes = wiz.selectedSuffix === null ? wiz.ops.slice(0, 5).map(function(op) { return op.suffix; }) : [wiz.selectedSuffix];
    if (!readOnly && !suffixes.length) { wizError('请选择认证后缀后验证登录。'); return; }
    var payload = wizConnection();
    payload.base_url = wiz.baseUrl; payload.ac_id = wiz.acId || '1'; payload.school = wiz.school;
    payload.user_id = wiz.userId; payload.password = readOnly ? '' : wiz.password;
    payload.max_attempts = suffixes.length; payload.candidates = JSON.stringify(suffixes);
    WIZ_SHAPE_KEYS.forEach(function(key) { payload[key] = wiz.shape[key] || ''; });
    wizInvalidate(); wiz.verifyExpanded = true; wiz.busy = 'operator'; wiz.error = '';
    wiz.opLog = readOnly ? '正在读取在线账号信息…' : '正在验证登录，请稍候…'; wizRender();
    wizPost('detect_operator', payload, function(err, data) {
      if (err) { wizError(err.message); return; }
      wiz.opLog = '';
      (data.attempts || []).forEach(function(attempt) {
        wiz.ops.forEach(function(op) { if (op.suffix === attempt.suffix) { op.outcome = attempt.outcome; op.message = attempt.message || ''; } });
        wiz.opLog += (attempt.username || '') + ' → ' + (WIZ_OUTCOME[attempt.outcome] || '验证未完成') + '\n';
      });
      if (data.confirmed) {
        wiz.selectedSuffix = data.suffix || ''; wiz.opConfirmed = true; wiz.passwordVerified = data.password_verified !== false;
        if (!wiz.ops.some(function(op) { return op.suffix === wiz.selectedSuffix; })) {
          wiz.ops.push({suffix: wiz.selectedSuffix, label: wiz.selectedSuffix || '不加后缀', src: '网关在线账号'});
        }
        wiz.ops.forEach(function(op) { if (op.suffix === wiz.selectedSuffix) {
          op.outcome = data.password_verified === false ? 'identity' : 'hit'; op.message = data.message || '';
        } });
      } else {
        wiz.error = data.message || '本次未能确认后缀，请查看具体结果。';
        wiz.ops.forEach(function(op) { if (!op.outcome && suffixes.indexOf(op.suffix) !== -1) {
          op.outcome = 'skipped'; op.message = '前置检查或上一项验证已停止，请先处理上方提示。';
        } });
      }
      wiz.opLog += data.message || '验证结束。'; wizRender();
    }, 180000);
  }

  function wizSummary(rows) {
    var table = wizEl('table', 'smart-wizard-summary');
    rows.forEach(function(row) {
      var tr = wizEl('tr'); tr.appendChild(wizEl('td', '', row[0])); tr.appendChild(wizEl('td', '', String(row[1]))); table.appendChild(tr);
    });
    return table;
  }

  function wizStepDone(body) {
    body.appendChild(wizAlert(wiz.opConfirmed ? 'success' : 'info', wiz.opConfirmed ?
      (wiz.passwordVerified ? '登录验证通过。' : '认证后缀已确认，密码尚未验证。') :
      '请确认以下账号与网络信息。'));
    body.appendChild(wizSummary([
      ['接入方式', wiz.accessMode === 'wired' ? '有线' : '无线'],
      ['出口接口', wizConnection().iface || '按已连接 Wi-Fi 自动匹配'],
      ['校园网 Wi-Fi', wiz.accessMode === 'wifi' ? (wiz.ssid || '尚未填写，请返回第一步补充') : '不适用'],
      ['学校预设', (findSchoolPreset(wiz.school) || {}).name || '无预设'],
      ['认证地址', wiz.baseUrl], ['AC_ID', wiz.acId || '1（默认值，尚未确认）'],
      ['账号类型', wizSelectedOperator().label], ['认证后缀', wiz.selectedSuffix ? '@' + wiz.selectedSuffix : '不加后缀'],
      ['登录名', wizLoginPreview(wiz.selectedSuffix)], ['密码', '已填写']
    ]));
    if (wiz.accessMode === 'wifi' && !wiz.ssid) {
      body.appendChild(wizAlert('warning', '请返回第一步填写校园网 Wi-Fi 名称，保存后才能自动匹配无线网络。'));
    }
    body.appendChild(wizActions(wiz.wifiJob ? '保存后设为默认账号，并启用此 Wi-Fi 的自动认证。' : '保存后可在账号列表中设为默认账号。', [
      { label: '修改接入方式', onclick: function() { wizGo(0); } },
      { label: '上一步', onclick: function() { wizGo(3); } },
      { label: wiz.busy ? '保存中…' : (wiz.wifiJob ? '保存并设为默认账号' : '保存为校园网账号'), cls: 'cbi-button-save', disabled: wiz.saved || (wiz.accessMode === 'wifi' && !wiz.ssid), onclick: wizSave }
    ]));
  }

  function wizContributionData() {
    var preset = findSchoolPreset(wiz.school) || {};
    var origin = /^(https?:\/\/)([^\/?#]+)/i.exec(wiz.baseUrl || '');
    var defaults = {
      base_url: origin ? origin[1] + origin[2].split('@').pop() : '',
      ac_id: wiz.acId || '', access_mode: wiz.accessMode
    };
    if (wiz.accessMode === 'wifi') defaults.ssid = wiz.ssid || '';
    var operators = [];
    var source = wiz.portalOps.length ? wiz.portalOps : wiz.ops;
    source.forEach(function(op) {
      if (typeof op.suffix !== 'string' || op.suffix === '??') return;
      if (operators.some(function(item) { return item.suffix === op.suffix; })) return;
      operators.push({ suffix: op.suffix, label: String(op.label || (op.suffix ? '@' + op.suffix : '无后缀')) });
    });
    if (!operators.some(function(op) { return op.suffix === wiz.selectedSuffix; })) {
      operators.push({ suffix: wiz.selectedSuffix || '', label: wizSelectedOperator().label });
    }
    // Export a new allowlisted object, never the wizard/account/config object.
    var result = { name: String(preset.name || ''), status: 'draft', defaults: defaults, operators: operators };
    if (preset.short_name) result.short_name = String(preset.short_name);
    return result;
  }

  function wizContributionBody() {
    var selected = wiz.selectedSuffix ? '@' + wiz.selectedSuffix : '无后缀';
    return '### 学校 / 校区\n\n' + wiz.contribution.name + '\n\n' +
      '### 插件使用结果\n\n' +
      '- 配置保存：成功\n' +
      '- 登录验证：' + (wiz.passwordVerified ? '已通过插件验证' : '未执行或未通过，保存不代表认证成功') + '\n' +
      '- 已确认校园网可正常使用：' + (wiz.contributionOnline ? '是（用户确认）' : '尚未确认') + '\n' +
      '- 本次所选后缀：' + selected + '\n' +
      '- 选项来源：' + (wiz.portalOps.length ? '认证页面' : (wiz.school ? '学校预设及手动补充' : '手动填写')) + '\n\n' +
      '### 预设参数\n\n```json\n' + JSON.stringify(wiz.contribution, null, 2) + '\n```\n\n' +
      '由智慧深澜一键配置生成。账号类型列表不表示全部类型均已登录验证；请维护者核对学校标识、验证范围与预设状态。';
  }

  function wizStepSaved(body) {
    body.appendChild(wizAlert('success', '配置已保存。'));
    body.appendChild(wizEl('p', '', '可提交学校预设，帮助其他用户完成配置。'));
    var name = wizEl('input'); name.id = 'wiz-contribution-name'; name.type = 'text';
    name.value = wiz.contribution.name; name.placeholder = '学校及校区名称';
    name.maxLength = 100;
    name.oninput = function() {
      wiz.contribution.name = name.value.replace(/^\s+|\s+$/g, '');
      if (wiz.contribution.name && wiz.error) {
        wiz.error = '';
        var error = document.getElementById('wiz-error');
        if (error && error.parentNode) error.parentNode.removeChild(error);
      }
      preview.value = wizContributionBody();
    };
    body.appendChild(wizRow('学校 / 校区', name));
    var confirmed = wizEl('input'); confirmed.type = 'checkbox';
    confirmed.id = 'wiz-contribution-online'; confirmed.checked = !!wiz.contributionOnline;
    confirmed.onchange = function() { wiz.contributionOnline = confirmed.checked; preview.value = wizContributionBody(); };
    var label = wizEl('label'); label.appendChild(confirmed);
    label.appendChild(document.createTextNode(' 已确认校园网可正常使用'));
    body.appendChild(label);
    var review = wizEl('details'); review.style.marginTop = '16px';
    review.appendChild(wizEl('summary', '', '预览提交内容'));
    var preview = wizEl('textarea'); preview.id = 'wiz-contribution-preview'; preview.readOnly = true;
    preview.rows = 12; preview.style.width = '100%'; preview.style.marginTop = '8px';
    preview.setAttribute('aria-label', 'Issue 草稿内容'); preview.value = wizContributionBody();
    review.appendChild(preview); body.appendChild(review);
    body.appendChild(wizEl('p', 'smart-wizard-help', '草稿仅包含学校、认证地址、SSID 与后缀信息，不包含账号、密码或设备标识。将在 GitHub 打开，由你确认后提交。'));
    body.appendChild(wizActions('', [{ label: '创建预设 Issue', cls: 'cbi-button-action', onclick: function() {
      if (!wiz.contribution.name) { wizError('请填写学校及校区名称。'); return; }
      var url = 'https://github.com/matthewlu070111/smart-srun/issues/new?title=' +
        encodeURIComponent('[School Preset]: ' + wiz.contribution.name) + '&body=' + encodeURIComponent(wizContributionBody());
      if (url.length > 7500) {
        review.open = true; preview.focus(); preview.select();
        body.appendChild(wizEl('p', 'smart-wizard-help', '草稿较长，请复制预览内容并粘贴到新 Issue。'));
        url = 'https://github.com/matthewlu070111/smart-srun/issues/new';
      }
      window.open(url, '_blank', 'noopener,noreferrer');
    } }]));
  }

  function wizSave() {
    if (wiz.busy || wiz.saved) return;
    if (!wiz.userId || !wiz.password || wiz.selectedSuffix === null || !wizNormalizeAddress()) { wizError('请检查账号、密码、认证后缀与认证地址。'); return; }
    if (!wiz.accessMode || (wiz.accessMode === 'wifi' && !wiz.ssid) || (wiz.accessMode === 'wired' && !wiz.wiredIface)) { wizError('请返回第一步补全接入信息。'); return; }
    var payload = {
      action: 'add_campus', label: wizLoginPreview(wiz.selectedSuffix), user_id: wiz.userId,
      operator_suffix: wiz.selectedSuffix, password: wiz.password, base_url: wiz.baseUrl,
      ac_id: wiz.acId || '1', access_mode: wiz.accessMode,
      wired_iface: wiz.wiredIface, ssid: wiz.accessMode === 'wifi' ? wiz.ssid : '', ap_selection: 'auto'
    };
    WIZ_SHAPE_KEYS.forEach(function(key) { payload[key] = wiz.shape[key] || ''; });
    wiz.busy = 'save'; wiz.error = ''; wizRender();
    payload.setup_job = wiz.wifiJob || '';
    wizPost('enqueue', payload, function(err, data) {
      if (data.saved) { wiz.saved = true; wiz.wifiJob = ''; }
      if (err || data.ok !== true) { wizError(err ? err.message : (data.message || '保存失败')); return; }
      wiz.wifiJob = '';
      wiz.saved = true; wiz.saveComplete = true;
      wiz.contribution = wizContributionData();
      wiz.userId = ''; wiz.password = ''; wiz.wifiKey = '';
      wizRender();
    });
  }

  function wizRender() {
    if (!wiz) return;
    wizStyles();
    var root = wizEl('div', 'smart-wizard');
    var steps = wizEl('ol', 'smart-wizard-steps');
    steps.setAttribute('aria-label', '配置步骤');
    WIZ_STEPS.forEach(function(text, index) {
      var item = wizEl('li');
      item.appendChild(document.createTextNode((index + 1) + '. '));
      item.appendChild(wizEl('span', 'smart-wizard-step-label', text));
      if (index === wiz.step) item.setAttribute('aria-current', 'step'); steps.appendChild(item);
    });
    root.appendChild(steps);
    var inner = wizEl('div', 'smart-wizard-content');
    if (wiz.error) { var error = wizAlert('danger', wiz.error); error.id = 'wiz-error'; inner.appendChild(error); }
    if (wiz.saveComplete) wizStepSaved(inner);
    else [wizStepEnv, wizStepAddress, wizStepOperator, wizStepAccount, wizStepDone][wiz.step](inner);
    root.appendChild(inner);
    var actions = inner.querySelector('.smart-wizard-actions');
    if (actions) root.appendChild(actions);
    if (wiz.busy) {
      var fields = inner.querySelectorAll('input,select,button');
      for (var i = 0; i < fields.length; i++) fields[i].disabled = true;
    }
    if (wiz.wifiJob && wiz.step === 0) {
      var connectionFields = inner.querySelectorAll('input,select');
      for (var k = 0; k < connectionFields.length; k++) connectionFields[k].disabled = true;
    }
    wiz.root = root;
    root.addEventListener('keydown', function(event) {
      if (event.keyCode === 27) {
        event.preventDefault(); event.stopPropagation(); wizClose();
      } else if (event.keyCode === 13 && event.target.tagName === 'INPUT') {
        // Never submit the underlying CBI form while typing in the wizard.
        event.preventDefault();
      } else if (event.keyCode === 9) {
        var focusable = root.querySelectorAll('input:not([disabled]),select:not([disabled]),textarea:not([disabled]),button:not([disabled]),summary');
        if (!focusable.length) { event.preventDefault(); return; }
        var first = focusable[0], last = focusable[focusable.length - 1];
        if (event.shiftKey && (event.target === first || event.target === root)) {
          event.preventDefault(); last.focus();
        } else if (!event.shiftKey && event.target === last) {
          event.preventDefault(); first.focus();
        }
      }
    });
    L.showModal('一键配置校园网', [root], 'smart-wizard-dialog');
    root.tabIndex = -1;
    root.focus();
  }

  function initSetupWizard() {
    var button = document.getElementById('smart-srun-setup-wizard');
    if (!button || window.__smartWizardInit) return;
    window.__smartWizardInit = true;
    button.addEventListener('click', function() { wizReset(); wizRender(); });
  }


  function initAll() {
    initVersionNotice();
    initTables();
    initSchoolInfo();
    initOverview();
    initManualActions();
    initSwitchActions();
    initSetupWizard();
    initLogView();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAll);
  } else {
    initAll();
  }
})();
