/* ============================================================
 * 项目服务内容管理子系统 · 高保真原型交互库
 * ============================================================ */
(function (global) {
  'use strict';

  // ---------- Toast 通知 ----------
  function ensureToastContainer() {
    let c = document.querySelector('.toast-container');
    if (!c) {
      c = document.createElement('div');
      c.className = 'toast-container';
      document.body.appendChild(c);
    }
    return c;
  }
  function toast(message, type = 'info', duration = 2400) {
    const container = ensureToastContainer();
    const el = document.createElement('div');
    el.className = 'toast ' + type;
    const icons = { success: '✓', error: '✕', warning: '⚠', info: 'ℹ' };
    el.innerHTML = '<span class="ico">' + (icons[type] || icons.info) + '</span><span class="body"></span><span class="close">✕</span>';
    el.querySelector('.body').textContent = message;
    container.appendChild(el);
    const close = () => {
      el.classList.add('leaving');
      setTimeout(() => el.remove(), 200);
    };
    el.querySelector('.close').addEventListener('click', close);
    if (duration > 0) setTimeout(close, duration);
    return close;
  }
  global.toast = toast;

  // ---------- Tab 切换 ----------
  function bindTabs(root) {
    (root || document).querySelectorAll('.tabs').forEach(tabs => {
      const items = tabs.querySelectorAll('.tab');
      items.forEach(tab => {
        if (tab.dataset.bound) return;
        tab.dataset.bound = '1';
        tab.addEventListener('click', () => {
          if (tab.classList.contains('active')) return;
          items.forEach(t => t.classList.remove('active'));
          tab.classList.add('active');
          // 切换面板（data-tab-target 指定的容器显示，其他隐藏）
          const target = tab.dataset.tabTarget;
          if (target) {
            document.querySelectorAll('[data-tab-panel]').forEach(p => {
              p.style.display = (p.dataset.tabPanel === target) ? '' : 'none';
            });
          }
          // 自定义事件
          tabs.dispatchEvent(new CustomEvent('tab-change', { detail: { tab, id: tab.dataset.tabId } }));
        });
      });
    });
  }
  global.bindTabs = bindTabs;

  // ---------- Checkbox 切换 ----------
  function bindCheckboxes(root) {
    (root || document).querySelectorAll('.checkbox').forEach(cb => {
      if (cb.dataset.bound) return;
      cb.dataset.bound = '1';
      cb.addEventListener('click', e => {
        e.preventDefault();
        if (cb.classList.contains('disabled')) return;
        cb.classList.toggle('checked');
        cb.dispatchEvent(new CustomEvent('change', { detail: { checked: cb.classList.contains('checked') } }));
      });
    });
  }
  global.bindCheckboxes = bindCheckboxes;

  // ---------- 开关 Switch ----------
  function bindSwitches(root) {
    (root || document).querySelectorAll('.switch').forEach(sw => {
      if (sw.dataset.bound) return;
      sw.dataset.bound = '1';
      sw.addEventListener('click', () => {
        sw.classList.toggle('on');
        sw.dispatchEvent(new CustomEvent('change', { detail: { on: sw.classList.contains('on') } }));
      });
    });
  }
  global.bindSwitches = bindSwitches;

  // ---------- 单选 Radio ----------
  function bindRadios(root) {
    (root || document).querySelectorAll('.radio-group').forEach(group => {
      if (group.dataset.bound) return;
      group.dataset.bound = '1';
      group.querySelectorAll('.radio').forEach(r => {
        r.addEventListener('click', () => {
          if (r.classList.contains('disabled')) return;
          group.querySelectorAll('.radio').forEach(x => x.classList.remove('checked'));
          r.classList.add('checked');
          group.dispatchEvent(new CustomEvent('change', { detail: { value: r.dataset.value || r.textContent.trim() } }));
        });
      });
    });
  }
  global.bindRadios = bindRadios;

  // ---------- 抽屉/弹窗 关闭 ----------
  function bindClosers(root) {
    (root || document).querySelectorAll('[data-close]').forEach(el => {
      if (el.dataset.bound) return;
      el.dataset.bound = '1';
      el.addEventListener('click', () => {
        const target = el.dataset.close;
        if (target === 'drawer') {
          const d = el.closest('.drawer');
          if (d) closeDrawer(d);
        } else if (target === 'mask') {
          const d = el.closest('.drawer');
          if (d) closeDrawer(d);
          else {
            const m = el.closest('.dialog-mask');
            if (m) closeDialog(m);
          }
        } else if (target === 'dialog') {
          const d = el.closest('.dialog-mask');
          if (d) closeDialog(d);
        }
      });
    });
    (root || document).querySelectorAll('.drawer-mask').forEach(m => {
      if (m.dataset.bound) return;
      m.dataset.bound = '1';
      m.addEventListener('click', () => {
        const d = m.querySelector('.drawer');
        if (d) closeDrawer(d);
      });
    });
    (root || document).querySelectorAll('.dialog-mask').forEach(m => {
      if (m.dataset.bound === '1') return;
      // 已在上面 .dialog-mask 点击关闭逻辑覆盖
    });
  }
  global.bindClosers = bindClosers;

  function closeDrawer(d) {
    const mask = d.previousElementSibling;
    d.style.animation = 'slideInRight 0.2s reverse';
    if (mask) mask.style.animation = 'fadeIn 0.2s reverse';
    setTimeout(() => {
      d.remove();
      if (mask) mask.remove();
    }, 200);
    toast('已关闭', 'info', 1500);
  }
  function closeDialog(m) {
    m.style.animation = 'fadeIn 0.2s reverse';
    setTimeout(() => m.remove(), 200);
  }
  global.closeDrawer = closeDrawer;
  global.closeDialog = closeDialog;

  // ---------- 树 Tree 节点展开/折叠 ----------
  function bindTrees(root) {
    (root || document).querySelectorAll('.tree .tree-toggle').forEach(t => {
      if (t.dataset.bound) return;
      t.dataset.bound = '1';
      t.style.cursor = 'pointer';
      t.addEventListener('click', e => {
        e.stopPropagation();
        t.classList.toggle('expanded');
        const parent = t.parentElement;
        const children = parent.querySelector(':scope > .tree-children');
        if (children) children.style.display = t.classList.contains('expanded') ? '' : 'none';
      });
    });
    (root || document).querySelectorAll('.tree .tree-node').forEach(n => {
      if (n.dataset.bound) return;
      n.dataset.bound = '1';
      n.addEventListener('click', () => {
        const tree = n.closest('.tree');
        tree.querySelectorAll('.tree-node').forEach(x => x.classList.remove('active'));
        n.classList.add('active');
        tree.dispatchEvent(new CustomEvent('select', { detail: { node: n } }));
      });
    });
  }
  global.bindTrees = bindTrees;

  // ---------- 表格行点击/选中 ----------
  function bindTableSelect(root) {
    (root || document).querySelectorAll('.crm-table').forEach(table => {
      if (table.dataset.bound) return;
      table.dataset.bound = '1';
      const checkAll = table.querySelector('thead .checkbox');
      if (checkAll) {
        checkAll.addEventListener('click', e => {
          e.stopPropagation();
          const checked = !checkAll.classList.contains('checked');
          checkAll.classList.toggle('checked', checked);
          if (checked) checkAll.classList.remove('indeterminate');
          table.querySelectorAll('tbody .checkbox').forEach(cb => {
            cb.classList.toggle('checked', checked);
            cb.closest('tr')?.classList.toggle('selected', checked);
          });
          updateBatchBar(table);
        });
      }
      table.querySelectorAll('tbody .checkbox').forEach(cb => {
        cb.addEventListener('click', e => {
          e.stopPropagation();
          const tr = cb.closest('tr');
          tr?.classList.toggle('selected');
          cb.classList.toggle('checked');
          // update check-all state
          if (checkAll) {
            const all = table.querySelectorAll('tbody .checkbox');
            const checked = table.querySelectorAll('tbody .checkbox.checked');
            checkAll.classList.toggle('checked', all.length === checked.length);
            checkAll.classList.toggle('indeterminate', checked.length > 0 && checked.length < all.length);
          }
          updateBatchBar(table);
        });
      });
    });
  }
  function updateBatchBar(table) {
    const checked = table.querySelectorAll('tbody .checkbox.checked').length;
    document.querySelectorAll('[data-batch-count]').forEach(b => {
      b.textContent = checked;
      b.parentElement.style.opacity = checked > 0 ? '1' : '0.5';
    });
  }
  global.bindTableSelect = bindTableSelect;

  // ---------- 行展开（折叠详情） ----------
  function bindRowExpand(root) {
    (root || document).querySelectorAll('.row-expand-trigger').forEach(t => {
      if (t.dataset.bound) return;
      t.dataset.bound = '1';
      t.addEventListener('click', e => {
        e.stopPropagation();
        const tr = t.closest('tr');
        t.classList.toggle('expanded');
        const next = tr.nextElementSibling;
        if (next && next.classList.contains('row-expand-content')) {
          next.style.display = t.classList.contains('expanded') ? 'table-row' : 'none';
        }
      });
    });
  }
  global.bindRowExpand = bindRowExpand;

  // ---------- 模拟操作反馈 ----------
  function bindAction(root) {
    (root || document).querySelectorAll('[data-action]').forEach(btn => {
      if (btn.dataset.bound) return;
      btn.dataset.bound = '1';
      btn.addEventListener('click', e => {
        if (btn.tagName === 'A' || btn.tagName === 'BUTTON') e.preventDefault();
        const action = btn.dataset.action;
        const messages = {
          'confirm': ['已确认', 'success'],
          'assign': ['指派完成 · 通知已发送', 'success'],
          'submit': ['已提交，等待审核', 'success'],
          'approve': ['已通过', 'success'],
          'reject': ['已驳回', 'warning'],
          'terminate': ['已终止 · 已联动合同补充协议', 'error'],
          'release': ['已释放', 'info'],
          'archive': ['已归档', 'success'],
          'export': ['已下载', 'info'],
          'sync': ['已同步', 'info'],
          'send': ['已发送', 'success'],
          'save': ['已保存', 'success'],
          'upload': ['上传成功', 'success'],
          'create': ['创建成功', 'success'],
          'delete': ['已删除', 'warning'],
          'edit': ['已进入编辑', 'info'],
          'force-pass': ['已带原因强制通过 · 审批留痕', 'warning'],
          'remind': ['已发送提醒', 'info'],
          'simulate-conflict': ['⚠ 检测到冲突（演示）', 'warning'],
          'resolve-conflict': ['✓ 冲突已解决', 'success'],
          'open-drawer': ['打开抽屉（演示）', 'info']
        };
        const m = messages[action];
        if (m) {
          toast(m[0], m[1]);
        } else {
          toast('操作：' + action, 'info');
        }
      });
    });
  }
  global.bindAction = bindAction;

  // ---------- 表单实时验证（演示） ----------
  function bindFormValidation(root) {
    (root || document).querySelectorAll('.form-item [data-validate]').forEach(input => {
      if (input.dataset.bound) return;
      input.dataset.bound = '1';
      const item = input.closest('.form-item');
      const type = input.dataset.validate;
      input.addEventListener('blur', () => {
        if (!input.value && input.required) {
          showError(item, '此项必填');
        } else if (type === 'phone' && !/^1\d{10}$/.test(input.value)) {
          showError(item, '请输入正确的手机号');
        } else if (type === 'email' && !/^[^@]+@[^@]+\.[^@]+$/.test(input.value)) {
          showError(item, '请输入正确的邮箱');
        } else {
          clearError(item);
        }
      });
    });
  }
  function showError(item, msg) {
    item.querySelector('.field-error')?.remove();
    const e = document.createElement('div');
    e.className = 'field-error';
    e.innerHTML = '<span>✕</span> ' + msg;
    item.appendChild(e);
    item.querySelector('input, textarea, select')?.classList.add('is-error');
  }
  function clearError(item) {
    item.querySelector('.field-error')?.remove();
    item.querySelector('input, textarea, select')?.classList.remove('is-error');
  }
  global.bindFormValidation = bindFormValidation;

  // ---------- 全局初始化 ----------
  function init() {
    bindTabs();
    bindCheckboxes();
    bindSwitches();
    bindRadios();
    bindClosers();
    bindTrees();
    bindTableSelect();
    bindRowExpand();
    bindAction();
    bindFormValidation();
  }

  // DOM ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  // 暴露给动态内容
  global.rebind = function (root) {
    bindTabs(root);
    bindCheckboxes(root);
    bindSwitches(root);
    bindRadios(root);
    bindClosers(root);
    bindTrees(root);
    bindTableSelect(root);
    bindRowExpand(root);
    bindAction(root);
    bindFormValidation(root);
  };
})(window);
