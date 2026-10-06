(() => {
  'use strict';

  const get = id => document.getElementById(id);
  const csrfToken = document.querySelector('meta[name="csrf-token"]').content;
  const form = get('ea-new-form');
  const state = {
    results: [], selected: null, ids: [], assets: new Map(), changes: new Map(),
    searchEpoch: 0, searchController: null, searchTimer: null,
    previewEpoch: 0, previewController: null, previewPending: false,
    saving: false, generating: false, total: '0 руб. 00 коп.'
  };

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // All money and record IDs stay strings; prices and totals are formatted by Go.
  async function request(url, { method = 'GET', body, signal } = {}) {
    const controller = new AbortController();
    let timedOut = false;
    const cancel = () => controller.abort();
    if (signal?.aborted) controller.abort();
    signal?.addEventListener('abort', cancel, { once: true });
    const timeout = setTimeout(() => { timedOut = true; controller.abort(); }, 15000);
    try {
      const headers = { Accept: 'application/json' };
      if (body !== undefined) {
        headers['Content-Type'] = 'application/json';
        headers['X-CSRF-Token'] = csrfToken;
      }
      const response = await fetch(url, {
        method, headers, credentials: 'same-origin', cache: 'no-store',
        body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal
      });
      let data;
      try { data = await response.json(); }
      catch { throw new Error('Сервер вернул непонятный ответ. Повторите запрос.'); }
      if (!response.ok) {
        const error = new Error(data.error || 'Не удалось выполнить запрос.');
        error.fields = data.fields || {};
        throw error;
      }
      return data;
    } catch (error) {
      if (timedOut) throw new Error('Сервер не ответил вовремя. Проверьте соединение и повторите запрос.');
      if (error.name === 'TypeError') throw new Error('Нет связи с сервером. Проверьте, что приложение запущено.');
      throw error;
    } finally {
      clearTimeout(timeout);
      signal?.removeEventListener('abort', cancel);
    }
  }

  const dateText = value => value ? value.split('-').reverse().join('.') : 'Не указана';
  const assetFields = asset => [
    ['Тип оборудования', asset.equipment_type],
    ['Модель', asset.model],
    ['Инвентарный номер 1С', asset.inventory_number],
    ['Серийный номер', asset.serial_number || 'Не указан'],
    ['Дата поступления', dateText(asset.received_date)],
    ['Сейчас числится за', asset.current_holder || 'Не указано'],
    ['Стоимость', asset.cost_display]
  ];

  async function refreshTypes() {
    try {
      const data = await request('/api/types');
      if (!Array.isArray(data.types)) throw new Error('Не удалось прочитать список типов оборудования.');
      const filter = get('ea-type-filter');
      const previous = filter.value;
      filter.replaceChildren(new Option('Все типы', ''));
      get('ea-type-list').replaceChildren();
      data.types.forEach(type => {
        filter.append(new Option(type, type));
        get('ea-type-list').append(new Option(type, type));
      });
      filter.value = previous;
      get('ea-types-error').hidden = true;
    } catch (error) {
      get('ea-types-error').textContent = 'Список типов не загружен. ' + error.message;
      get('ea-types-error').hidden = false;
    }
  }

  function renderSelected() {
    const asset = state.selected;
    get('ea-selection').hidden = !asset;
    if (!asset) return;
    const details = get('ea-details');
    details.replaceChildren();
    assetFields(asset).forEach(([label, value]) => {
      const block = element('div', label === 'Стоимость' ? 'ea-cost' : '');
      block.append(element('dt', '', label), element('dd', '', value));
      details.append(block);
    });
    const added = state.ids.includes(asset.id);
    get('ea-add-document').disabled = added;
    get('ea-add-document').textContent = added ? 'Уже в акте' : 'Добавить в акт';
    get('ea-selection-status').textContent = 'Выбрано: ' + asset.inventory_number + (added ? ' · Уже добавлено в акт' : '');
  }

  function renderResults(focusId) {
    const results = get('ea-results');
    focusId ||= results.contains(document.activeElement) ? document.activeElement.dataset.assetId : undefined;
    results.replaceChildren();
    results.hidden = state.results.length === 0;
    state.results.forEach(asset => {
      const chosen = asset.id === state.selected?.id;
      const result = element('button', 'ea-result');
      result.type = 'button';
      result.dataset.assetId = asset.id;
      result.setAttribute('aria-pressed', String(chosen));
      result.setAttribute('aria-label', 'Выбрать ' + asset.model + ', ' + asset.inventory_number + ', серийный номер ' + (asset.serial_number || 'не указан'));
      result.append(
        element('span', 'ea-result-main', asset.model),
        element('span', 'ea-result-meta', asset.current_holder || 'Ответственный не указан'),
        element('span', 'ea-result-meta', asset.equipment_type + ' · ' + asset.inventory_number + ' · S/N: ' + (asset.serial_number || '—')),
        element('span', 'ea-result-meta', chosen ? 'Выбрано' : 'Выбрать')
      );
      result.addEventListener('click', () => {
        state.selected = state.results.find(item => item.id === asset.id) || null;
        renderResults(asset.id);
        renderSelected();
      });
      results.append(result);
      if (focusId === asset.id) result.focus({ preventScroll: true });
    });
  }

  function scheduleSearch(immediate = false) {
    clearTimeout(state.searchTimer);
    state.searchController?.abort();
    const epoch = ++state.searchEpoch;
    state.selected = null;
    state.results = [];
    renderSelected();
    renderResults();
    get('ea-search-retry').hidden = true;
    get('ea-search-status').className = 'ea-status ea-search-status';
    const query = get('ea-search').value.trim();
    const type = get('ea-type-filter').value;
    if (!query && !type) {
      get('ea-results').setAttribute('aria-busy', 'false');
      get('ea-search-status').textContent = 'Введите запрос или выберите тип оборудования.';
      return;
    }
    get('ea-search-status').textContent = 'Поиск оборудования…';
    get('ea-results').setAttribute('aria-busy', 'true');
    const run = async () => {
      const controller = new AbortController();
      state.searchController = controller;
      try {
        const params = new URLSearchParams({ q: query, type });
        const data = await request('/api/assets?' + params, { signal: controller.signal });
        if (epoch !== state.searchEpoch) return;
        if (!Array.isArray(data.assets)) throw new Error('Не удалось прочитать результаты поиска.');
        state.results = data.assets;
        renderResults();
        get('ea-search-status').textContent = data.assets.length
          ? 'Найдено: ' + data.assets.length + (data.more ? '. Есть другие результаты — уточните запрос.' : '. Выберите конкретный экземпляр.')
          : 'Ничего не найдено. Можно добавить новое оборудование.';
      } catch (error) {
        if (epoch !== state.searchEpoch || error.name === 'AbortError') return;
        get('ea-search-status').textContent = error.message;
        get('ea-search-status').className = 'ea-error ea-search-status';
        get('ea-search-retry').hidden = false;
      } finally {
        if (epoch === state.searchEpoch) get('ea-results').setAttribute('aria-busy', 'false');
      }
    };
    if (immediate) void run();
    else state.searchTimer = setTimeout(run, 250);
  }

  function clearErrors() {
    get('ea-form-error').hidden = true;
    form.querySelectorAll('.ea-field-error').forEach(node => { node.textContent = ''; });
    form.querySelectorAll('[aria-invalid]').forEach(node => node.removeAttribute('aria-invalid'));
  }

  function showErrors(error) {
    get('ea-form-error').textContent = error.message;
    get('ea-form-error').hidden = false;
    let first;
    Object.entries(error.fields || {}).forEach(([field, message]) => {
      const control = form.elements.namedItem(field);
      const hint = get('ea-error-' + field);
      if (!control || !hint) return;
      hint.textContent = String(message);
      control.setAttribute('aria-invalid', 'true');
      if (!first && !control.disabled) first = control;
    });
    first?.focus();
  }

  function showNew(open) {
    get('ea-picker').hidden = open;
    form.hidden = !open;
    get('ea-open-new').hidden = open;
    get('ea-equipment-heading').textContent = open ? 'Новое оборудование' : 'Выбор оборудования';
    clearErrors();
    get('ea-save-status').textContent = '';
    if (open) get('ea-new-type').focus();
  }

  function updateVatFields() {
    const withoutVat = get('ea-new-vat-mode').value === 'none';
    get('ea-new-rate-field').hidden = withoutVat;
    get('ea-new-vat-field').hidden = withoutVat;
    get('ea-new-rate').required = !withoutVat;
    get('ea-new-vat').required = !withoutVat;
    get('ea-new-rate').disabled = withoutVat;
    get('ea-new-vat').disabled = withoutVat;
  }

  // Normalize decimal punctuation without converting to a JavaScript Number.
  function decimal(value) {
    return value.trim().replace(/[\s\u00a0\u202f]/g, '').replace(',', '.');
  }

  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (state.saving) return;
    clearErrors();
    const data = Object.fromEntries(new FormData(form));
    ['equipment_type', 'model', 'inventory_number', 'serial_number', 'current_holder'].forEach(field => { data[field] = data[field].trim(); });
    data.price_rub = decimal(data.price_rub);
    data.vat_rate = data.vat_mode === 'none' ? '' : decimal(data.vat_rate);
    data.vat_rub = data.vat_mode === 'none' ? '0.00' : decimal(data.vat_rub);
    state.saving = true;
    get('ea-new-fields').disabled = true;
    get('ea-save').textContent = 'Сохранение…';
    get('ea-save-status').textContent = 'Сохраняем запись в базу…';
    let failure;
    try {
      const response = await request('/api/assets', { method: 'POST', body: data });
      if (!response.asset?.id) throw new Error('Сервер не вернул созданную запись. Найдите её по инвентарному номеру перед повторным сохранением.');
      clearTimeout(state.searchTimer);
      state.searchController?.abort();
      ++state.searchEpoch;
      const asset = response.asset;
      state.selected = asset;
      state.results = [asset];
      get('ea-type-filter').value = '';
      get('ea-search').value = asset.inventory_number;
      get('ea-search-status').className = 'ea-status ea-search-status';
      get('ea-search-status').textContent = 'Новая запись сохранена и выбрана.';
      get('ea-search-retry').hidden = true;
      get('ea-results').setAttribute('aria-busy', 'false');
      showNew(false);
      renderResults();
      renderSelected();
      form.reset();
      updateVatFields();
      get('ea-live-status').textContent = 'Новая запись выбрана. Нажмите «Добавить в акт», чтобы включить её в документ.';
      get('ea-add-document').focus();
      void refreshTypes();
    } catch (error) {
      failure = error;
    } finally {
      state.saving = false;
      get('ea-new-fields').disabled = false;
      get('ea-save').textContent = 'Сохранить в базу';
      get('ea-save-status').textContent = '';
      if (failure) showErrors(failure);
    }
  });

  function renderChanges() {
    const container = get('ea-changes');
    container.replaceChildren();
    container.hidden = state.changes.size === 0;
    if (!state.changes.size) return;
    container.append(element('strong', '', 'Реквизиты в базе изменились. В акте показаны свежие данные.'));
    const list = element('ul');
    state.changes.forEach((changes, id) => {
      const asset = state.assets.get(id);
      changes.forEach(change => list.append(element('li', '', (asset?.inventory_number || id) + ' — ' + change)));
    });
    container.append(list);
  }

  function renderDocument() {
    updateGenerateButton();
    const container = get('ea-document-items');
    const focusedId = container.contains(document.activeElement) ? document.activeElement.dataset.assetId : undefined;
    container.replaceChildren();
    state.ids.forEach(id => {
      const asset = state.assets.get(id);
      const row = element('div', 'ea-doc-item');
      const description = element('div');
      description.append(
        element('div', 'ea-doc-model', asset.model),
        element('div', 'ea-doc-meta', asset.equipment_type + ' · ' + asset.inventory_number + ' · S/N: ' + (asset.serial_number || '—'))
      );
      const remove = element('button', '', 'Убрать');
      remove.type = 'button';
      remove.dataset.assetId = id;
      remove.setAttribute('aria-label', 'Убрать из акта ' + asset.inventory_number);
      remove.addEventListener('click', () => {
        state.ids = state.ids.filter(item => item !== id);
        state.assets.delete(id);
        state.changes.delete(id);
        renderDocument();
        renderSelected();
        void refreshPreview();
        get('ea-live-status').textContent = asset.inventory_number + ' убрано из акта. Запись в базе сохранена.';
        const next = container.querySelector('button');
        (next || get('ea-open-new')).focus();
      });
      row.append(description, element('span', 'ea-item-cost', asset.cost_display), remove);
      container.append(row);
      if (focusedId === id) remove.focus({ preventScroll: true });
    });
    if (!state.ids.length) container.append(element('div', 'ea-status', 'Выберите устройство и нажмите «Добавить в акт».'));
    get('ea-count').textContent = 'Позиций: ' + state.ids.length;
    get('ea-total').textContent = state.total;
    get('ea-refresh-act').hidden = state.ids.length === 0;
    renderChanges();
  }

  async function refreshPreview() {
    state.previewController?.abort();
    const epoch = ++state.previewEpoch;
    const status = get('ea-preview-status');
    status.className = 'ea-status';
    if (!state.ids.length) {
      state.previewPending = false;
      state.total = '0 руб. 00 коп.';
      status.textContent = '';
      get('ea-refresh-act').disabled = false;
      renderDocument();
      return;
    }
    const ids = [...state.ids];
    const controller = new AbortController();
    state.previewController = controller;
    state.previewPending = true;
    updateGenerateButton();
    state.total = 'Проверяем…';
    status.textContent = 'Проверяем реквизиты и итог по базе…';
    get('ea-refresh-act').disabled = true;
    get('ea-total').textContent = state.total;
    try {
      const data = await request('/api/act/preview', { method: 'POST', body: { ids }, signal: controller.signal });
      if (epoch !== state.previewEpoch) return;
      if (!Array.isArray(data.assets) || data.assets.length !== ids.length || typeof data.total_display !== 'string') {
        throw new Error('Сервер не подтвердил все позиции акта. Обновите данные.');
      }
      const fresh = new Map(data.assets.map(asset => [asset.id, asset]));
      if (fresh.size !== ids.length || ids.some(id => !fresh.has(id))) throw new Error('Состав ответа не совпал со списком акта. Обновите данные.');
      ids.forEach(id => {
        const previous = state.assets.get(id);
        const asset = fresh.get(id);
        if (previous && previous.revision !== asset.revision) {
          const oldFields = assetFields(previous);
          const changes = assetFields(asset).flatMap(([label, value], index) => value === oldFields[index][1] ? [] : [label + ': «' + oldFields[index][1] + '» → «' + value + '».']);
          if (changes.length) state.changes.set(id, [...(state.changes.get(id) || []), ...changes]);
        }
        state.assets.set(id, asset);
        const resultIndex = state.results.findIndex(item => item.id === id);
        if (resultIndex !== -1) state.results[resultIndex] = asset;
        if (state.selected?.id === id) state.selected = asset;
      });
      state.total = data.total_display;
      status.textContent = 'Реквизиты и итог проверены по базе.';
      renderDocument();
      renderResults();
      renderSelected();
    } catch (error) {
      if (epoch !== state.previewEpoch || error.name === 'AbortError') return;
      state.total = 'Не подтверждена';
      get('ea-total').textContent = state.total;
      status.className = 'ea-error';
      status.textContent = error.message + ' Реквизиты и итог акта не подтверждены.';
    } finally {
      if (epoch === state.previewEpoch) {
        state.previewPending = false;
        updateGenerateButton();
        get('ea-refresh-act').disabled = false;
      }
    }
  }

  get('ea-search').addEventListener('input', () => scheduleSearch());
  get('ea-type-filter').addEventListener('change', () => scheduleSearch(true));
  get('ea-search-retry').addEventListener('click', () => { void refreshTypes(); scheduleSearch(true); });
  get('ea-open-new').addEventListener('click', () => showNew(true));
  get('ea-cancel-new').addEventListener('click', () => {
    form.reset();
    updateVatFields();
    showNew(false);
    get('ea-open-new').focus();
  });
  get('ea-new-vat-mode').addEventListener('change', updateVatFields);
  get('ea-add-document').addEventListener('click', () => {
    const asset = state.selected;
    if (!asset || state.ids.includes(asset.id)) return;
    state.ids.push(asset.id);
    state.assets.set(asset.id, asset);
    renderDocument();
    renderSelected();
    void refreshPreview();
    get('ea-live-status').textContent = 'Оборудование добавлено в акт. Можно выбрать следующую позицию.';
  });
  get('ea-refresh-act').addEventListener('click', () => { void refreshPreview(); });
  function updateGenerateButton() {
    get('ea-generate').disabled = state.generating || state.previewPending || state.ids.length === 0;
    get('ea-generate').textContent = state.generating ? 'Формируем…' : 'Сформировать Word';
  }

  get('ea-generate').addEventListener('click', async () => {
    const status = get('ea-word-status');
    const number = get('ea-number').value.trim();
    const date = get('ea-date').value;
    const recipient = get('ea-recipient').value.trim();
    status.className = 'ea-status ea-template-note';
    if (!number || !date || !recipient) {
      status.className = 'ea-error';
      status.textContent = 'Заполните номер акта, дату и получателя.';
      (!number ? get('ea-number') : !date ? get('ea-date') : get('ea-recipient')).focus();
      return;
    }
    const ids = [...state.ids];
    const revisions = Object.fromEntries(ids.map(id => [id, state.assets.get(id)?.revision || '']));
    state.generating = true;
    updateGenerateButton();
    status.textContent = 'Проверяем данные в базе и формируем Word…';
    try {
      const response = await fetch('/api/act/document', {
        method: 'POST', credentials: 'same-origin', cache: 'no-store',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ ids, revisions, number, date, recipient }),
        signal: AbortSignal.timeout(30000)
      });
      if (!response.ok) {
        const data = await response.json();
        if (response.status === 409) await refreshPreview();
        throw new Error(data.error || 'Не удалось сформировать Word.');
      }
      if (!response.headers.get('Content-Type')?.startsWith('application/vnd.openxmlformats-officedocument.wordprocessingml.document')) {
        throw new Error('Сервер вернул неожиданный формат файла.');
      }
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = 'act-' + date + '.docx';
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 30000);
      status.textContent = 'Word сформирован. Скачивание начато; текущий ответственный не изменён.';
    } catch (error) {
      status.className = 'ea-error';
      status.textContent = error.name === 'TimeoutError' ? 'Формирование заняло слишком много времени. Повторите запрос.' :
        error.name === 'TypeError' ? 'Нет связи с сервером. Повторите запрос.' : error.message;
    } finally {
      state.generating = false;
      updateGenerateButton();
    }
  });
  window.addEventListener('focus', () => {
    if (state.ids.length && !state.previewPending && !state.saving) void refreshPreview();
  });

  updateVatFields();
  void refreshTypes();
})();
