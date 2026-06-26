(function() {
  if (window.__smartSrunUiLoaded) return;
  window.__smartSrunUiLoaded = true;

  var campusData = [];
  var hotspotData = [];
  var modalType = '';
  var modalEditId = '';
  var modalSaveHandler = null;
  var RELEASES_API_URL = 'https://api.github.com/repos/matthewlu070111/smart-srun/releases/latest';
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

  function normalizeVersionText(value) {
    var text = String(value == null ? '' : value).trim();
    var match = text.match(/^v?([^-]+)-r?(\d+)$/);
    if (!match) {
      match = text.match(/^v?(\d+(?:\.\d+)+)$/);
      if (!match) return '';
      return { base: match[1], release: 0 };
    }
    return { base: match[1], release: parseInt(match[2], 10) || 0 };
  }

  function compareVersionParts(left, right) {
    var leftParts = String(left || '0').split('.');
    var rightParts = String(right || '0').split('.');
    var length = Math.max(leftParts.length, rightParts.length);
    for (var i = 0; i < length; i++) {
      var leftNum = parseInt(leftParts[i] || '0', 10) || 0;
      var rightNum = parseInt(rightParts[i] || '0', 10) || 0;
      if (leftNum !== rightNum) return leftNum - rightNum;
    }
    return 0;
  }

  function isRemoteNewer(localVersion, remoteTag) {
    var localInfo = normalizeVersionText(localVersion);
    var remoteInfo = normalizeVersionText(remoteTag);
    if (!localInfo || !remoteInfo) return false;
    var baseCompare = compareVersionParts(localInfo.base, remoteInfo.base);
    if (baseCompare !== 0) return baseCompare < 0;
    return (localInfo.release || 0) < (remoteInfo.release || 0);
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

  function openBlockingFeedback(action, requestedAt) {
    var result = document.getElementById('smart-srun-manual-result') || document.getElementById('smart-srun-switch-result');
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

    function unlock(text, success) {
      if (closed) return;
      closed = true;
      if (timer) window.clearInterval(timer);
      setTerminalFooter();
      if (result && text) result.textContent = text + (success ? ' 🎉' : ' ⚠');
    }

    function checkTerminal(statusData) {
      if (!statusData) return false;
      if (statusData.last_action !== action) return false;
      if ((statusData.last_action_ts || 0) < requestedAt) return false;
      if (statusData.action_result === 'forced') {
        unlock(statusData.status || '已强制停止', false);
        return true;
      }
      if (statusData.action_result === 'error') {
        unlock(statusData.status || '执行失败', false);
        return true;
      }
      if (statusData.action_result === 'ok') {
        unlock(statusData.status || '操作完成', true);
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

    L.showModal(titles[action] || '正在执行动作', [ tip, logBox, footer ], 'cbi-modal');
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
    setRowDisabled('jm-ssid-row', 'jm-ssid', wired);
    setRowDisabled('jm-bssid-row', 'jm-bssid', wired);
    setRowDisabled('jm-radio-row', 'jm-radio', wired);
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
    var items = readJson('smart-school-preset-data', []);
    return items && items.length ? items : [{
      short_name: 'jxnu',
      name: '江西师范大学',
      defaults: { base_url: 'http://172.17.1.2', ac_id: '1', ssid: 'jxnu_stu' },
      observed_login_shape: { n: '200', type: '1', enc: 'srun_bx1', info_prefix: 'SRBX1', double_stack: '0', os: 'Windows 10', name: 'Windows' },
      operators: [
        {suffix:'cmcc', label:'中国移动'},
        {suffix:'ctcc', label:'中国电信'},
        {suffix:'cucc', label:'中国联通'},
        {suffix:'', label:'校园网'}
      ]
    }];
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
    var items = schoolPresetList();
    var wanted = String(id || 'jxnu');
    if (wanted === '__none__') return null;
    for (var i = 0; i < items.length; i++) {
      if (String(items[i].short_name || '') === wanted) return items[i];
    }
    for (var j = 0; j < items.length; j++) {
      if (String(items[j].short_name || '') === 'jxnu') return items[j];
    }
    return items[0] || null;
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
      var message = '已保存默认配置';
      if (xhr.status === 200) {
        try {
          var data = JSON.parse(xhr.responseText || '{}');
          if (typeof data.message === 'string' && data.message !== '')
            message = data.message;
        } catch (e) {}
      }
      alert(message);
      location.reload();
    };
    xhr.send(fd);
  };

  window.smartDelete = function(kind, id) {
    if (!confirm('确定要删除此项吗？')) return;
    var fd = new FormData();
    fd.append('action', 'delete_' + kind);
    fd.append('id', id);
    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.onload = function() { location.reload(); };
    xhr.send(fd);
  };

  window.smartEditCampus = function(id) {
    modalType = 'campus';
    modalEditId = id;
    var item = id ? findById(campusData, id) : {};
    var presets = schoolPresetList();
    var NO_PRESET_ID = '__none__';
    var selectedPresetId = 'jxnu';
    var selectedPreset = findSchoolPreset(selectedPresetId);
    var presetApplied = false;
    var defaultOperators = [
      {suffix:'cmcc', label:'中国移动'},
      {suffix:'ctcc', label:'中国电信'},
      {suffix:'cucc', label:'中国联通'},
      {suffix:'', label:'校园网'}
    ];
    var initialValues = {
      label: item.label || '',
      user_id: item.user_id || '',
      operator_suffix: item.operator_suffix || '',
      access_mode: item.access_mode || 'wifi',
      base_url: item.base_url || 'http://172.17.1.2',
      ac_id: item.ac_id || '1',
      n: item.n || '',
      type: item.type || '',
      enc: item.enc || '',
      info_prefix: item.info_prefix || '',
      double_stack: item.double_stack || '',
      login_os: item.login_os || '',
      login_name: item.login_name || '',
      ssid: item.ssid || 'jxnu_stu',
      bssid: item.bssid || '',
      radio: item.radio || ''
    };

    // 运营商后缀的快捷下拉只在学校预设提供了 operators 时展示；没有则只保留可编辑的后缀输入框。
    function presetOperators(preset) {
      return (preset && preset.operators && preset.operators.length) ? preset.operators : [];
    }

    // 字段已从 id 改名为 suffix；仍兼容旧的 id 键。
    function operatorSuffixOf(op) {
      if (!op) return '';
      var v = (op.suffix !== undefined && op.suffix !== null) ? op.suffix : op.id;
      return String(v || '');
    }

    function operatorOptionsMarkup(ops, selectedSuffix) {
      var out = '';
      var selectedText = String(selectedSuffix || '');
      for (var oi = 0; oi < ops.length; oi++) {
        var sfx = operatorSuffixOf(ops[oi]);
        var label = String(ops[oi].label || sfx || '校园网');
        // suffix 为 "??" 表示该运营商后缀尚未被提供者验证，下拉里标注“未验证”。
        var text = (sfx === '??') ? (label + '（未验证）') : label;
        var selected = (sfx === selectedText) ? ' selected' : '';
        out += '<option value="' + escapeHtml(sfx) + '"' + selected + '>' + escapeHtml(text) + '</option>';
      }
      return out;
    }

    // 根据当前预设刷新快捷下拉的可见性与选项。
    function refreshOperatorQuickpick(preset, selectedSuffix) {
      var wrap = document.getElementById('jm-operator-quickpick-wrap');
      var opSel = document.getElementById('jm-operator');
      if (!wrap || !opSel) return;
      var ops = presetOperators(preset);
      if (ops.length) {
        opSel.innerHTML = operatorOptionsMarkup(ops, selectedSuffix);
        wrap.style.display = '';
      } else {
        opSel.innerHTML = '';
        wrap.style.display = 'none';
      }
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

    function presetOptionsMarkup() {
      var out = '<option value="' + NO_PRESET_ID + '"' + (selectedPresetId === NO_PRESET_ID ? ' selected' : '') + '>无预设</option>';
      for (var pi = 0; pi < presets.length; pi++) {
        var presetId = String(presets[pi].short_name || '');
        if (!presetId) continue;
        out += '<option value="' + escapeHtml(presetId) + '"' + (presetId === selectedPresetId ? ' selected' : '') + '>' + escapeHtml(presets[pi].name || presetId) + '</option>';
      }
      return out;
    }

    var initialOperators = presetOperators(selectedPreset);
    var quickpickDisplay = initialOperators.length ? '' : 'none';

    var bodyHtml =
      '<div class="smart-native-row"><label>学校预设</label><span><select id="jm-school_preset">' + presetOptionsMarkup() + '</select> <button type="button" id="jm-apply-school-defaults" class="btn cbi-button cbi-button-action">应用预设</button> <button type="button" id="jm-reset-school-defaults" class="btn cbi-button">复位</button></span></div>' +
      '<div class="smart-native-row"><label>标签（选填）</label><input id="jm-label" value="' + escapeHtml(initialValues.label) + '"></div>' +
      '<div class="smart-native-row"><label>学工号</label><input id="jm-user_id" value="' + escapeHtml(initialValues.user_id) + '"></div>' +
      '<div class="smart-native-row"><label>运营商后缀 <a href="https://github.com/matthewlu070111/smart-srun#%E8%8E%B7%E5%8F%96%E5%AD%A6%E9%A2%84%E8%AE%BE%E4%B8%8E%E8%BF%90%E8%90%A5%E5%95%86%E5%90%8E%E7%BC%80" target="_blank" rel="noopener noreferrer">如何获取？</a></label>' +
        '<span id="jm-operator-quickpick-wrap" style="display:' + quickpickDisplay + ';margin-bottom:.35rem;"><select id="jm-operator">' + operatorOptionsMarkup(initialOperators, initialValues.operator_suffix) + '</select></span>' +
        '<input id="jm-operator_suffix" value="' + escapeHtml(initialValues.operator_suffix) + '" placeholder="">' +
        '<div id="jm-operator-suffix-hint" style="display:none;color:#d97706;font-size:12px;margin-top:.25rem;"></div></div>' +
      '<div class="smart-native-row"><label>接入方式</label><select id="jm-access_mode"><option value="wifi"' + (initialValues.access_mode === 'wifi' ? ' selected' : '') + '>无线</option><option value="wired"' + (initialValues.access_mode === 'wired' ? ' selected' : '') + '>有线（WAN）</option></select></div>' +
      '<div class="smart-native-row"><label>密码</label><div id="jm-password-field"></div></div>' +
      '<div class="smart-native-row"><label>认证地址</label><input id="jm-base_url" value="' + escapeHtml(initialValues.base_url) + '"></div>' +
      '<div class="smart-native-row"><label>AC_ID</label><span><input id="jm-ac_id" value="' + escapeHtml(initialValues.ac_id) + '"> <button type="button" id="jm-detect-acid" class="btn cbi-button">嗅探</button> <span id="jm-detect-acid-status" style="margin-left:6px;color:#6b7280;"></span></span></div>' +
      '<details class="smart-native-advanced"><summary>高级登录参数</summary>' +
      '<div class="smart-native-row"><label>n</label><input id="jm-login-n" value="' + escapeHtml(initialValues.n) + '" placeholder="200"></div>' +
      '<div class="smart-native-row"><label>type</label><input id="jm-login-type" value="' + escapeHtml(initialValues.type) + '" placeholder="1"></div>' +
      '<div class="smart-native-row"><label>enc</label><input id="jm-login-enc" value="' + escapeHtml(initialValues.enc) + '" placeholder="srun_bx1"></div>' +
      '<div class="smart-native-row"><label>info 前缀</label><input id="jm-info-prefix" value="' + escapeHtml(initialValues.info_prefix) + '" placeholder="SRBX1"></div>' +
      '<div class="smart-native-row"><label>double_stack</label><input id="jm-double-stack" value="' + escapeHtml(initialValues.double_stack) + '" placeholder="0"></div>' +
      '<div class="smart-native-row"><label>os</label><input id="jm-login-os" value="' + escapeHtml(initialValues.login_os) + '" placeholder="Windows 10"></div>' +
      '<div class="smart-native-row"><label>name</label><input id="jm-login-name" value="' + escapeHtml(initialValues.login_name) + '" placeholder="Windows"></div>' +
      '</details>' +
      '<div class="smart-native-row" id="jm-ssid-row"><label>校园网 SSID</label><input id="jm-ssid" value="' + escapeHtml(initialValues.ssid) + '"></div>' +
      '<div class="smart-native-row" id="jm-bssid-row"><label>BSSID（留空则不锁定）</label><input id="jm-bssid" value="' + escapeHtml(initialValues.bssid) + '"></div>' +
      '<div class="smart-native-row" id="jm-radio-row"><label>频段</label><select id="jm-radio">' + radioOptionsMarkup() + '</select></div>';

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
      var nextOperators = presetOperators(preset);
      var nextSuffix = nextOperators.length ? operatorSuffixOf(nextOperators[0]) : '';
      refreshOperatorQuickpick(preset, nextSuffix);
      applyLoginShapeToForm(loginShape);
      presetApplied = true;
      // 应用预设时，若该预设提供了运营商，则用第一个运营商联动填充后缀；否则保持手填。
      if (nextOperators.length) {
        var opSelApply = document.getElementById('jm-operator');
        if (opSelApply) opSelApply.value = nextSuffix;
        applyOperatorPick();
      }
      if (schoolDefaults.access_mode) {
        var modeSel = document.getElementById('jm-access_mode');
        if (modeSel) modeSel.value = String(schoolDefaults.access_mode);
      } else {
        var fallbackModeSel = document.getElementById('jm-access_mode');
        if (fallbackModeSel && selectedPresetId === NO_PRESET_ID) fallbackModeSel.value = 'wifi';
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
      presetApplied = false;
      selectedPresetId = NO_PRESET_ID;
      selectedPreset = findSchoolPreset(selectedPresetId);
      var presetSel = document.getElementById('jm-school_preset');
      if (presetSel) presetSel.value = selectedPresetId;
      // 无预设：隐藏运营商快捷下拉，仅保留手填后缀输入框。
      refreshOperatorQuickpick(null, '');
      var resetHint = document.getElementById('jm-operator-suffix-hint');
      if (resetHint) { resetHint.textContent = ''; resetHint.style.display = 'none'; }
      var values = {
        'jm-label': initialValues.label,
        'jm-user_id': initialValues.user_id,
        'jm-operator_suffix': '',
        'jm-access_mode': 'wifi',
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
        if (data.ok && data.acid) {
          if (acidInput) acidInput.value = data.acid;
          if (baseInput && data.base_url) baseInput.value = data.base_url;
          if (statusNode) statusNode.textContent = '已填入 ' + data.acid;
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
        document.getElementById('jm-school_preset').addEventListener('change', function() {
          presetApplied = false;
          selectedPresetId = this.value || NO_PRESET_ID;
          selectedPreset = findSchoolPreset(selectedPresetId);
        });
        document.getElementById('jm-apply-school-defaults').addEventListener('click', applySchoolDefaultsToForm);
        document.getElementById('jm-reset-school-defaults').addEventListener('click', resetSchoolDefaultsForm);
        document.getElementById('jm-detect-acid').addEventListener('click', detectAcidForForm);
        document.getElementById('jm-access_mode').addEventListener('change', updateCampusAccessModeUI);
        document.getElementById('jm-operator').addEventListener('change', applyOperatorPick);
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
    var fd = new FormData();
    fd.append('action', (modalEditId ? 'edit_' : 'add_') + modalType);
    if (modalEditId) fd.append('id', modalEditId);

    if (modalType === 'campus') {
      fd.append('label', document.getElementById('jm-label').value);
      fd.append('user_id', document.getElementById('jm-user_id').value);
      fd.append('operator_suffix', document.getElementById('jm-operator_suffix').value);
      fd.append('access_mode', document.getElementById('jm-access_mode').value);
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
      L.hideModal();
      location.reload();
    };
    xhr.send(fd);
  };

  function initSchoolInfo() {
    var infoBox = document.getElementById('smart-school-info');
    var docLinkEl = document.getElementById('smart-school-doc-link');
    if (!infoBox || !docLinkEl || window.__smartSchoolInfoInit) return;
    window.__smartSchoolInfoInit = true;

    var DOC_BASE = 'https://github.com/matthewlu070111/smart-srun/blob/main/doc/';
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
      docLinkEl.href = DOC_BASE + encodeURIComponent(String(value || '')) + '.md';
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

  function findLogLevelSelect() {
    return document.getElementById('widget.cbid.smart_srun.main.log_level')
      || document.getElementById('cbid.smart_srun.main.log_level')
      || document.querySelector('select[name="cbid.smart_srun.main.log_level"]');
  }

  function initLogView() {
    var box = document.getElementById('smart-srun-log-box');
    var pre = document.getElementById('smart-srun-log-pre');
    var channels = document.getElementById('smart-srun-log-channels');
    var startButton = document.getElementById('smart-srun-log-start');
    var stopButton = document.getElementById('smart-srun-log-stop');
    var clearButton = document.getElementById('smart-srun-log-clear');
    var downloadButton = document.getElementById('smart-srun-log-download');
    if (!box || !pre || !channels || !startButton || !stopButton || !clearButton || !downloadButton || window.__smartSrunLogInit) return;
    window.__smartSrunLogInit = true;
    var channelButtons = channels.getElementsByTagName('button');
    var levelSelect = findLogLevelSelect();
    var logState = {
      channel: 'plugin',
      refreshing: true,
      timer: null,
      rawText: pre.textContent || '',
      displayLevel: (levelSelect && levelSelect.value) ? String(levelSelect.value).toUpperCase() : 'ALL'
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

    function setChannelButtons() {
      for (var i = 0; i < channelButtons.length; i++) {
        var button = channelButtons[i];
        var active = button.getAttribute('data-channel') === logState.channel;
        button.className = active ? 'cbi-button cbi-button-action' : 'cbi-button cbi-button-neutral';
      }
    }

    function buildLogUrl(lines, format, download) {
      return '/cgi-bin/luci/admin/services/smart_srun/log_tail?channel=' +
        encodeURIComponent(logState.channel) + '&lines=' + lines +
        '&format=' + encodeURIComponent(format || 'friendly') +
        (download ? '&download=1' : '') + '&_=' + Date.now();
    }

    function buildDownloadName() {
      var now = new Date();
      function pad(value) { return value < 10 ? '0' + value : String(value); }
      return 'smart_srun_' + logState.channel + '_' + now.getFullYear() +
        pad(now.getMonth() + 1) + pad(now.getDate()) + '_' +
        pad(now.getHours()) + pad(now.getMinutes()) + pad(now.getSeconds()) + '.log';
    }

    function refresh() {
      if (isPageHidden()) return;
      fetchJson(buildLogUrl(LOG_LIVE_LINES, 'friendly', false), function(err, data) {
        if (err || !data || typeof data.log !== 'string') return;
        if (data.channel && data.channel !== logState.channel) return;
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
      pre.innerHTML = '';
      logState.rawText = '';
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

    for (var i = 0; i < channelButtons.length; i++) {
      channelButtons[i].addEventListener('click', function() {
        var nextChannel = this.getAttribute('data-channel') || 'plugin';
        if (nextChannel !== 'plugin' && nextChannel !== 'network') nextChannel = 'plugin';
        if (logState.channel === nextChannel) return;
        logState.channel = nextChannel;
        setChannelButtons();
        refresh();
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

    function readLevelFromEvent(ev) {
      var t = ev && ev.target;
      if (!t || !t.tagName) return null;
      var id = t.id || '';
      var name = (t.getAttribute && t.getAttribute('name')) || '';
      var dataName = (t.getAttribute && t.getAttribute('data-name')) || '';
      if (id.indexOf('log_level') === -1 &&
          name.indexOf('log_level') === -1 &&
          dataName.indexOf('log_level') === -1) return null;
      if (t.value != null && t.value !== '') return t.value;
      var dv = t.getAttribute && t.getAttribute('data-value');
      return dv != null ? dv : null;
    }

    document.addEventListener('change', function(ev) {
      var v = readLevelFromEvent(ev);
      if (v != null) applyDisplayLevel(v);
    }, true);
    document.addEventListener('cbi-dropdown-change', function(ev) {
      var v = readLevelFromEvent(ev);
      if (v != null) applyDisplayLevel(v);
    }, true);
    if (levelSelect) {
      levelSelect.addEventListener('change', function() {
        applyDisplayLevel(levelSelect.value);
      });
    }

    setChannelButtons();
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
