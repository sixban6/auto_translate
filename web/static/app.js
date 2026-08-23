document.addEventListener("DOMContentLoaded", () => {
  const fileInput = document.getElementById("fileInput");
  const selectedFileName = document.getElementById("selectedFileName");
  const uploadZone = document.getElementById("uploadZone");
  const topBar = document.querySelector(".top-bar");
  const dashboard = document.getElementById("dashboard");
  const startBtn = document.getElementById("startBtn");
  const downloadBtn = document.getElementById("downloadBtn");
  const pauseBtn = document.getElementById("pauseBtn");
  const stopExportBtn = document.getElementById("stopExportBtn");
  const configForm = document.getElementById("configForm");
  const terminalLog = document.getElementById("terminalLog");
  const completionChimeInput = configForm.querySelector(
    'input[name="completion_chime"]'
  );

  let currentFile = null;
  let eventSource = null;
  let heartbeatInterval = null;
  let audioContext = null;
  let chimePlayedTaskId = "";
  let currentTaskId = "";

  const historyBtn = document.getElementById("historyBtn");
  const historyModal = document.getElementById("historyModal");
  const closeHistoryBtn = document.getElementById("closeHistoryBtn");
  const refreshHistoryBtn = document.getElementById("refreshHistoryBtn");
  const historyTableBody = document.getElementById("historyTableBody");
  const historyEmptyState = document.getElementById("historyEmptyState");

  // Last task list rendered in the history modal; drives select-all,
  // batch delete and clear-all.
  let lastTaskList = [];
  const selectedTaskIds = new Set();

  function openHistory() {
    if (!historyModal) return;
    historyModal.classList.remove("hidden");
    fetchTasks();
  }

  function closeHistory() {
    if (!historyModal) return;
    historyModal.classList.add("hidden");
  }

  if (historyBtn) historyBtn.addEventListener("click", openHistory);
  if (closeHistoryBtn) closeHistoryBtn.addEventListener("click", closeHistory);
  if (refreshHistoryBtn)
    refreshHistoryBtn.addEventListener("click", fetchTasks);

  const batchDeleteBtn = document.getElementById("batchDeleteBtn");
  if (batchDeleteBtn)
    batchDeleteBtn.addEventListener("click", () =>
      batchDeleteTasks([...selectedTaskIds])
    );
  const clearAllTasksBtn = document.getElementById("clearAllTasksBtn");
  if (clearAllTasksBtn)
    clearAllTasksBtn.addEventListener("click", clearAllTasks);
  const selectAllTasksBox = document.getElementById("selectAllTasks");
  if (selectAllTasksBox) {
    selectAllTasksBox.addEventListener("change", () => {
      if (selectAllTasksBox.checked) {
        lastTaskList.forEach((t) => selectedTaskIds.add(t.id));
      } else {
        selectedTaskIds.clear();
      }
      renderHistoryTable(lastTaskList);
      updateBatchBar();
    });
  }

  if (historyModal) {
    historyModal.addEventListener("click", (e) => {
      if (e.target === historyModal) {
        closeHistory();
      }
    });
  }

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeHistory();
  });

  function saveHistory(config) {
    localStorage.setItem(
      "auto_trans_config",
      JSON.stringify({
        engine: config.engine,
        api_url: config.api_url,
        max_chunk_size: config.max_chunk_size,
        concurrency: config.concurrency,
        request_timeout_sec: config.request_timeout_sec,
        max_retries: config.max_retries,
        bilingual: config.bilingual,
        chapter_batching: config.chapter_batching,
        completion_chime: completionChimeInput.checked,
      })
    );
  }

  function loadHistory() {
    try {
      const saved = localStorage.getItem("auto_trans_config");
      if (saved) {
        const conf = JSON.parse(saved);
        if (conf.engine && engineSelect) {
          engineSelect.value = conf.engine;
          applyEngineDefaults(conf.engine, false);
        }
        if (conf.api_url) {
          // Normalize any legacy default address to the current default of
          // the saved (or inferred) engine; custom URLs are kept as-is.
          let savedUrl = conf.api_url;
          if (isEngineDefaultURL(savedUrl)) {
            const engine =
              conf.engine && engineDefaults[conf.engine]
                ? conf.engine
                : engineOfDefaultURL(savedUrl) || "omlx";
            savedUrl = engineDefaults[engine];
          }
          document.querySelector('input[name="api_url"]').value = savedUrl;
        }
        if (conf.request_timeout_sec)
          document.querySelector('input[name="request_timeout_sec"]').value =
            conf.request_timeout_sec;
        if (conf.max_retries)
          document.querySelector('input[name="max_retries"]').value =
            conf.max_retries;
        if (conf.bilingual !== undefined)
          document.querySelector('input[name="bilingual"]').checked =
            conf.bilingual;
        const chapterToggle = configForm.querySelector(
          'input[name="chapter_batching"]'
        );
        if (chapterToggle && conf.chapter_batching !== undefined)
          chapterToggle.checked = conf.chapter_batching;
        if (conf.completion_chime !== undefined)
          completionChimeInput.checked = conf.completion_chime;
        const chunkSelect = document.getElementById("chunkSizeSelect");
        if (conf.max_chunk_size !== undefined && chunkSelect) {
          chunkSelect.value = String(conf.max_chunk_size);
          if (chunkSelect.value !== String(conf.max_chunk_size)) {
            // Saved value has no matching option (e.g. legacy): fall back to auto.
            chunkSelect.value = "0";
          }
        }
        const concSelect = document.getElementById("concurrencySelect");
        if (conf.concurrency !== undefined && concSelect) {
          concSelect.value = String(conf.concurrency);
          if (concSelect.value !== String(conf.concurrency)) {
            // Saved value has no matching option: fall back to auto.
            concSelect.value = "0";
          }
        }
      }
    } catch (e) {
      console.warn("Failed to load history", e);
    }
  }

  const engineDefaults = {
    omlx: "http://127.0.0.1:8000/v1",
    mlx: "http://127.0.0.1:8080/v1/chat/completions",
    ollama: "http://localhost:11434/v1/chat/completions",
  };

  const engineSelect = document.getElementById("engineSelect");
  const apiURLInput = document.querySelector('input[name="api_url"]');

  // Legacy default URL forms from earlier versions; used to normalize saved
  // settings so stale addresses never resurface after an upgrade.
  const legacyDefaultURLs = [
    "http://127.0.0.1:8000/v1/chat/completions",
    "http://127.0.0.1:8080/v1/chat/completions",
    "http://localhost:11434/v1/chat/completions",
    "http://localhost:11434",
  ];

  function isEngineDefaultURL(u) {
    return (
      !u ||
      Object.values(engineDefaults).includes(u) ||
      legacyDefaultURLs.includes(u)
    );
  }

  function engineOfDefaultURL(u) {
    for (const [engine, url] of Object.entries(engineDefaults)) {
      if (u === url) return engine;
    }
    for (const legacy of legacyDefaultURLs) {
      if (u === legacy) {
        if (legacy.includes(":8000")) return "omlx";
        if (legacy.includes(":8080")) return "mlx";
        if (legacy.includes(":11434")) return "ollama";
      }
    }
    return null;
  }

  // Switch the API URL to the engine's default endpoint unless the user has
  // customized it for the current engine.
  function applyEngineDefaults(engine, resetModel) {
    const prevEngine =
      engineSelect.dataset.prevEngine ||
      engineSelect.querySelector("option")?.value ||
      "omlx";
    const wasDefault = isEngineDefaultURL(apiURLInput.value);
    if (wasDefault) {
      apiURLInput.value = engineDefaults[engine] || engineDefaults.omlx;
    }
    engineSelect.dataset.prevEngine = engine;
    if (resetModel) {
      fetchModels(false);
    }
  }

  if (engineSelect) {
    engineSelect.dataset.prevEngine = engineSelect.value;
    engineSelect.addEventListener("change", (e) => {
      applyEngineDefaults(e.target.value, true);
    });
  }

  loadHistory();

  const modelSelect = document.getElementById("modelSelect");
  const modelInput = document.getElementById("modelInput");
  const roleSelect = document.getElementById("roleSelect");

  function engineLabel(engine) {
    if (engine === "ollama") return "Ollama";
    if (engine === "omlx") return "oMLX";
    return "MLX";
  }

  // Refresh the "auto" option label of the chunk-size select with the
  // recommended batch size of the currently selected model.
  function updateChunkAutoLabel(recommended) {
    const chunkSelect = document.getElementById("chunkSizeSelect");
    if (!chunkSelect) return;
    const autoOpt = chunkSelect.querySelector('option[value="0"]');
    if (!autoOpt) return;
    if (recommended > 0) {
      autoOpt.textContent = `自动（当前模型推荐：${recommended} 字符）`;
    } else {
      autoOpt.textContent = "自动（跟随模型推荐）";
    }
  }

  // Refresh the "auto" option label of the concurrency select with the
  // backend's live recommendation (RAM / model aware).
  function updateConcurrencyAutoLabel(recommended) {
    const concSelect = document.getElementById("concurrencySelect");
    if (!concSelect) return;
    const autoOpt = concSelect.querySelector('option[value="0"]');
    if (!autoOpt) return;
    if (recommended > 0) {
      autoOpt.textContent = `自动（当前推荐：${recommended}）`;
    } else {
      autoOpt.textContent = "自动（跟随智能规划）";
    }
  }

  // Debounced explain-config probe; keeps the concurrency recommendation
  // label fresh and the dashboard explanation in sync.
  function scheduleExplain(delay = 500) {
    clearTimeout(window.explainTimeout);
    window.explainTimeout = setTimeout(fetchExplanation, delay);
  }

  modelSelect.addEventListener("change", (e) => {
    if (e.target.value === "__custom__") {
      modelInput.style.display = "block";
      modelInput.value = "";
      return;
    }
    modelInput.style.display = "none";
    modelInput.value = e.target.value;
    // Picking a model served by the other engine switches engine + URL.
    const opt = e.target.selectedOptions && e.target.selectedOptions[0];
    const optEngine = opt && opt.dataset.engine;
    const recSize = opt && parseInt(opt.dataset.chunkSize || "0", 10);
    if (recSize) updateChunkAutoLabel(recSize);
    if (optEngine && engineSelect && engineSelect.value !== optEngine) {
      engineSelect.value = optEngine;
      applyEngineDefaults(optEngine, false);
    }
    // Engine/model may have changed: refresh the concurrency recommendation.
    scheduleExplain(300);
  });

  // Fetch Models Function
  // useAuto=true probes every supported local engine (oMLX, MLX, Ollama) via
  // the backend and merges the results; otherwise the given api_url is probed.
  async function fetchModels(useAuto) {
    try {
      const apiUrl = apiURLInput.value.trim();
      const requestUrl = useAuto
        ? "/api/models"
        : `/api/models?api_url=${encodeURIComponent(apiUrl)}`;
      const res = await fetch(requestUrl);
      if (res.ok) {
        const data = await res.json();
        const models = (data.models || []).filter((m) => m && m.name);
        if (models.length > 0) {
          const currentEngine = engineSelect ? engineSelect.value : "omlx";
          const hasCurrentEngine = models.some(
            (m) => (m.engine || currentEngine) === currentEngine
          );
          // Models of the active engine come first.
          models.sort(
            (a, b) =>
              (a.engine === currentEngine ? 0 : 1) -
              (b.engine === currentEngine ? 0 : 1)
          );

          if (
            useAuto &&
            !hasCurrentEngine &&
            data.detected_engine &&
            engineSelect
          ) {
            // The selected engine has nothing running; switch to the one
            // that answered automatically.
            engineSelect.value = data.detected_engine;
            applyEngineDefaults(data.detected_engine, false);
            showToast(
              `已自动检测到 ${engineLabel(data.detected_engine)} 引擎`,
              "success"
            );
          }

          modelSelect.innerHTML = "";
          let firstRecommended = 0;
          models.forEach((m) => {
            const opt = document.createElement("option");
            opt.value = m.name;
            const eng = m.engine || currentEngine;
            opt.textContent =
              eng === (engineSelect ? engineSelect.value : "omlx")
                ? m.name
                : `${m.name}（${engineLabel(eng)}）`;
            opt.dataset.engine = eng;
            if (m.chunk_size) {
              opt.dataset.chunkSize = String(m.chunk_size);
              if (!firstRecommended) firstRecommended = m.chunk_size;
            }
            modelSelect.appendChild(opt);
          });
          updateChunkAutoLabel(firstRecommended);

          // Models/engine changed: refresh the concurrency recommendation
          // label too (the programmatic change event below does not bubble
          // to the form listener).
          scheduleExplain(400);

          const customOpt = document.createElement("option");
          customOpt.value = "__custom__";
          customOpt.textContent = "➕ 自定义手动输入...";
          modelSelect.appendChild(customOpt);

          // Initialize hidden input
          modelSelect.value = models[0].name;
          modelSelect.dispatchEvent(new Event("change"));
        } else {
          modelSelect.innerHTML =
            useAuto
              ? '<option value="__custom__">未检测到本地模型 (手动输入)</option>'
              : '<option value="__custom__">未检测到模型 (手动输入)</option>';
          modelSelect.value = "__custom__";
          modelSelect.dispatchEvent(new Event("change"));
        }
      }
    } catch (e) {
      console.warn("Failed to fetch models", e);
      modelSelect.innerHTML =
        '<option value="__custom__">无法连接推理服务 (手动输入)</option>';
      modelSelect.value = "__custom__";
      modelSelect.dispatchEvent(new Event("change"));
    }
  }

  // Initialize models list: auto-detect engines when the URL is still a
  // known default; probe the custom URL directly otherwise.
  fetchModels(isEngineDefaultURL(apiURLInput.value.trim()));
  apiURLInput.addEventListener("blur", () => {
    fetchModels(false);
    scheduleExplain(300);
  });

  async function fetchRoles() {
    try {
      const res = await fetch("/api/roles");
      if (res.ok) {
        const data = await res.json();
        if (data.roles && data.roles.length > 0) {
          let globalRoles = [];
          roleSelect.innerHTML = "";
          data.roles.forEach((role) => {
            const opt = document.createElement("option");
            opt.value = role.name;
            opt.textContent = role.name;
            roleSelect.appendChild(opt);
          });
          globalRoles = data.roles;

          const rolePreview = document.getElementById("rolePreview");
          roleSelect.addEventListener("change", () => {
            const selected = globalRoles.find(
              (r) => r.name === roleSelect.value
            );
            if (selected && selected.preview) {
              rolePreview.textContent = selected.preview;
              rolePreview.style.display = "block";
            } else {
              rolePreview.style.display = "none";
            }
          });

          if (data.roles.find((r) => r.name === "金融翻译专家")) {
            roleSelect.value = "金融翻译专家";
            roleSelect.dispatchEvent(new Event("change"));
          } else if (data.roles.length > 0) {
            roleSelect.dispatchEvent(new Event("change"));
          }
        }
      }
    } catch (e) {
      console.warn("Failed to fetch roles", e);
    }
  }

  fetchRoles();

  function statusText(status, reason) {
    if (status === "running") return "进行中";
    if (status === "queued") return "排队中";
    if (status === "interrupted") return "可恢复";
    if (status === "paused") return "已暂停";
    if (status === "completed") {
      if (reason === "stopped_partial") return "部分完成";
      return "已完成";
    }
    if (status === "error") return "失败";
    return status || "未知";
  }

  function getStatusClass(status) {
    if (status === "running") return "status-running";
    if (status === "queued") return "status-queued";
    if (status === "interrupted") return "status-interrupted";
    if (status === "paused") return "status-paused";
    if (status === "completed") return "status-completed";
    if (status === "error") return "status-error";
    return "status-queued";
  }

  function renderHistoryTable(tasks) {
    if (!historyTableBody) return;
    historyTableBody.innerHTML = "";

    // Drop selections that no longer exist (e.g. deleted elsewhere).
    const visibleIds = new Set((tasks || []).map((t) => t.id));
    for (const id of [...selectedTaskIds]) {
      if (!visibleIds.has(id)) selectedTaskIds.delete(id);
    }

    if (!tasks || tasks.length === 0) {
      if (historyEmptyState) historyEmptyState.classList.remove("hidden");
      updateBatchBar();
      return;
    }
    if (historyEmptyState) historyEmptyState.classList.add("hidden");

    const statusPriority = {
      running: 0,
      queued: 1,
      interrupted: 2,
      paused: 2,
      completed: 3,
      error: 4,
    };

    tasks.sort((a, b) => {
      const pA = statusPriority[a.status] ?? 99;
      const pB = statusPriority[b.status] ?? 99;
      if (pA !== pB) return pA - pB;
      return (b.updated_at || 0) - (a.updated_at || 0);
    });

    tasks.forEach((task) => {
      const tr = document.createElement("tr");

      const checkTd = document.createElement("td");
      const cb = document.createElement("input");
      cb.type = "checkbox";
      cb.checked = selectedTaskIds.has(task.id);
      cb.onclick = (e) => e.stopPropagation();
      cb.onchange = () => {
        if (cb.checked) selectedTaskIds.add(task.id);
        else selectedTaskIds.delete(task.id);
        updateBatchBar();
      };
      checkTd.appendChild(cb);
      tr.appendChild(checkTd);

      const nameTd = document.createElement("td");
      const fileName =
        task.src_file_name ||
        (task.input_path || "").split("/").pop() ||
        `Unknown Task ${task.id}`;
      const nameDiv = document.createElement("div");
      nameDiv.style.fontWeight = "500";
      nameDiv.textContent = fileName;
      const idSpan = document.createElement("div");
      idSpan.style.fontSize = "11px";
      idSpan.style.color = "var(--text-muted)";
      idSpan.textContent = task.id;
      nameTd.appendChild(nameDiv);
      nameTd.appendChild(idSpan);

      const statusTd = document.createElement("td");
      const displayStatus =
        task.total > 0 && task.current >= task.total
          ? "completed"
          : task.status;
      const badge = document.createElement("span");
      badge.className = `status-badge ${getStatusClass(displayStatus)}`;
      badge.textContent = statusText(displayStatus, task.status_reason);
      statusTd.appendChild(badge);

      const progressTd = document.createElement("td");
      if (task.total > 0) {
        const percent = Math.round((task.current / task.total) * 100);
        const bar = document.createElement("div");
        bar.className = "progress-bar";
        bar.style.height = "6px";
        bar.style.marginBottom = "4px";
        bar.style.backgroundColor = "rgba(255,255,255,0.1)";
        const fill = document.createElement("div");
        fill.className = "progress-fill";
        fill.style.width = `${percent}%`;
        bar.appendChild(fill);
        const text = document.createElement("div");
        text.style.fontSize = "12px";
        text.style.color = "var(--text-muted)";
        text.textContent = `${percent}% (${task.current}/${task.total})`;
        progressTd.appendChild(bar);
        progressTd.appendChild(text);
      } else {
        const text = document.createElement("div");
        text.style.fontSize = "12px";
        text.style.color = "var(--text-muted)";
        text.textContent = "计算中...";
        progressTd.appendChild(text);
      }

      const actionsTd = document.createElement("td");
      const actionWrapper = document.createElement("div");
      actionWrapper.className = "flex";
      actionWrapper.style.gap = "8px";

      if (task.status === "running" || task.status === "queued") {
        const pauseBtnItem = document.createElement("button");
        pauseBtnItem.className = "btn-secondary btn-sm";
        pauseBtnItem.textContent = "暂停";
        pauseBtnItem.onclick = (e) => {
          e.stopPropagation();
          pauseTask(task.id);
        };
        actionWrapper.appendChild(pauseBtnItem);

        const viewBtn = document.createElement("button");
        viewBtn.className = "btn-secondary btn-sm";
        viewBtn.textContent = "查看";
        viewBtn.onclick = (e) => {
          e.stopPropagation();
          openTask(task);
          closeHistory();
        };
        actionWrapper.appendChild(viewBtn);
      }

      if (
        task.status === "interrupted" ||
        task.status === "paused" ||
        task.status === "error"
      ) {
        const resumeBtn = document.createElement("button");
        resumeBtn.className = "btn-primary btn-sm";
        resumeBtn.textContent = "继续";
        resumeBtn.onclick = (e) => {
          e.stopPropagation();
          resumeTask(task);
          closeHistory();
        };
        actionWrapper.appendChild(resumeBtn);
      }

      if (
        task.status === "running" ||
        task.status === "queued" ||
        task.status === "paused"
      ) {
        const stopBtnItem = document.createElement("button");
        stopBtnItem.className = "btn-secondary btn-sm";
        stopBtnItem.style.color = "var(--text-red)";
        stopBtnItem.style.borderColor = "var(--text-red)";
        stopBtnItem.textContent = "终止导出";
        stopBtnItem.onclick = (e) => {
          e.stopPropagation();
          stopExportTask(task.id);
        };
        actionWrapper.appendChild(stopBtnItem);
      }

      const canDownload = displayStatus === "completed";
      if (canDownload) {
        const downloadBtnItem = document.createElement("button");
        downloadBtnItem.className = "btn-secondary btn-sm";
        downloadBtnItem.textContent = "下载";
        downloadBtnItem.onclick = (e) => {
          e.stopPropagation();
          window.location.href = `/api/download?task_id=${task.id}`;
        };
        actionWrapper.appendChild(downloadBtnItem);
      }

      const deleteBtn = document.createElement("button");
      deleteBtn.className = "btn-danger btn-sm";
      deleteBtn.textContent = "删除";
      deleteBtn.onclick = (e) => {
        e.stopPropagation();
        deleteTask(task.id);
      };
      actionWrapper.appendChild(deleteBtn);

      actionsTd.appendChild(actionWrapper);

      tr.appendChild(checkTd);
      tr.appendChild(nameTd);
      tr.appendChild(statusTd);
      tr.appendChild(progressTd);
      tr.appendChild(actionsTd);

      historyTableBody.appendChild(tr);
    });
    updateBatchBar();
  }

  // Sync the history modal's batch toolbar (selection count, buttons,
  // select-all checkbox) with the current selection state.
  function updateBatchBar() {
    const total = lastTaskList.length;
    const n = selectedTaskIds.size;
    const info = document.getElementById("historySelectionInfo");
    if (info)
      info.textContent = total === 0 ? "暂无任务" : `已选 ${n} / ${total} 项`;
    if (batchDeleteBtn) {
      batchDeleteBtn.textContent = `批量删除 (${n})`;
      batchDeleteBtn.disabled = n === 0;
    }
    if (clearAllTasksBtn) clearAllTasksBtn.disabled = total === 0;
    if (selectAllTasksBox) {
      selectAllTasksBox.disabled = total === 0;
      selectAllTasksBox.checked = total > 0 && n === total;
    }
  }

  async function batchDeleteTasks(ids) {
    if (!ids.length) return;
    const ok = window.confirm(
      `确定删除选中的 ${ids.length} 个任务及关联文件？进行中的翻译将被中止并清除，此操作不可逆转。`
    );
    if (!ok) return;
    try {
      const res = await fetch("/api/tasks", {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids }),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const failed = (data.failed || []).length;
      showToast(
        failed
          ? `已删除 ${data.deleted || 0} 个任务，${failed} 个删除失败`
          : `已删除 ${data.deleted || 0} 个任务`,
        failed ? "info" : "success"
      );
      if (ids.includes(currentTaskId)) resetUI("任务已删除");
      selectedTaskIds.clear();
      fetchTasks();
    } catch (e) {
      showToast("批量删除失败: " + e.message, "error");
    }
  }

  async function clearAllTasks() {
    if (!lastTaskList.length) {
      showToast("没有可清空的历史任务", "info");
      return;
    }
    const ok = window.confirm(
      `确定清空全部 ${lastTaskList.length} 个历史任务？进行中的翻译将被中止并清除，此操作不可逆转。`
    );
    if (!ok) return;
    try {
      const res = await fetch("/api/tasks", { method: "DELETE" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const failed = (data.failed || []).length;
      showToast(
        failed
          ? `已清空 ${data.deleted || 0} 个任务，${failed} 个删除失败`
          : "已清空全部历史任务",
        failed ? "info" : "success"
      );
      if (lastTaskList.some((t) => t.id === currentTaskId))
        resetUI("任务已删除");
      selectedTaskIds.clear();
      fetchTasks();
    } catch (e) {
      showToast("清空失败: " + e.message, "error");
    }
  }

  async function deleteTask(taskId) {
    const ok = window.confirm("是否彻底删除该任务及关联文件？进行中的翻译将被中止并清除，此操作不可逆转。");
    if (!ok) return;
    try {
      const res = await fetch(`/api/tasks/${taskId}`, { method: "DELETE" });
      if (!res.ok) throw new Error(await res.text());
      if (currentTaskId === taskId) {
        resetUI("任务已删除");
      }
      showToast("任务已删除", "success");
      fetchTasks();
    } catch (e) {
      showToast("删除失败: " + e.message, "error");
    }
  }

  async function fetchTasks() {
    try {
      const res = await fetch("/api/tasks");
      if (!res.ok) return;
      const data = await res.json();
      lastTaskList = data.tasks || [];
      renderHistoryTable(lastTaskList);
    } catch (e) {
      console.warn("fetch tasks failed", e);
    }
  }

  async function pauseTask(taskId) {
    try {
      const res = await fetch(`/api/pause?task_id=${taskId}`, {
        method: "POST",
      });
      if (!res.ok) throw new Error(await res.text());
      showToast("任务已暂停", "success");
      fetchTasks();
    } catch (e) {
      showToast("暂停失败: " + e.message, "error");
    }
  }

  async function stopExportTask(taskId) {
    if (
      !confirm(
        "确定终止翻译并导出当前进度吗？未翻译的部分将保留原文，任务将就此结束。"
      )
    ) {
      return;
    }
    try {
      const res = await fetch(`/api/stop_export?task_id=${taskId}`, {
        method: "POST",
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      if (data.status === "stopping") {
        showToast("正在终止翻译并导出部分译本...", "info");
      } else {
        showToast(
          `已终止并导出（已翻译 ${data.translated}/${data.total} 段）`,
          "success"
        );
        if (stopExportBtn) stopExportBtn.classList.add("hidden");
        if (pauseBtn) pauseBtn.classList.add("hidden");
      }
      fetchTasks();
    } catch (e) {
      showToast("终止导出失败: " + e.message, "error");
    }
  }

  async function resumeTask(task) {
    try {
      const config = await resumeTaskById(task.id);
      openTask(task);
      showToast(`已开始恢复任务（模型: ${config.model}）`, "success");
    } catch (e) {
      showToast("恢复失败: " + e.message, "error");
    }
  }

  // Resume a task with the config currently shown in the sidebar, so the
  // user can switch model/engine/parameters between pause and resume.
  async function resumeTaskById(taskId) {
    const config = buildConfigFromForm();
    const res = await fetch(`/api/resume?task_id=${taskId}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(config),
    });
    if (!res.ok) throw new Error(await res.text());
    return config;
  }

  function openTask(task) {
    resetDashboardForNewTask();
    currentTaskId = task.id;
    dashboard.classList.remove("hidden");
    const displayName =
      task.src_file_name || (task.input_path || "").split("/").pop() || task.id;
    document.getElementById("taskTitle").textContent = `正在翻译: ${displayName}`;
    const badge = document.getElementById("statusBadge");
    if (task.status === "queued") {
      badge.textContent = "排队中";
      badge.style.backgroundColor = "rgba(139, 148, 158, 0.12)";
      badge.style.color = "var(--text-muted)";
    } else {
      badge.textContent = "执行中";
      badge.style.backgroundColor = "rgba(248, 81, 73, 0.1)";
      badge.style.color = "var(--text-red)";
    }
    startBtn.classList.add("hidden");
    downloadBtn.classList.add("hidden");
    if (pauseBtn) {
      pauseBtn.classList.remove("hidden");
      pauseBtn.onclick = () => pauseTask(task.id);
    }
    if (stopExportBtn) {
      stopExportBtn.classList.remove("hidden");
      stopExportBtn.onclick = () => stopExportTask(task.id);
    }
    log(`已切换到历史任务 ${displayName}（ID: ${task.id}）`, "gray");
    listenToProgress(task.id);
  }

  fetchTasks();

  // Toast Notification System
  function showToast(message, type = "info") {
    const container = document.getElementById("toastContainer");
    const toast = document.createElement("div");
    toast.className = `toast ${type}`;
    toast.textContent = message;
    container.appendChild(toast);
    setTimeout(() => toast.remove(), 4000);
  }

  // Terminal Logger
  function log(message, type = "gray") {
    const line = document.createElement("div");
    line.className = `log-line text-${type}`;
    const time = new Date().toLocaleTimeString("en-US", { hour12: false });
    line.textContent = `[${time}] ${message}`;
    terminalLog.appendChild(line);
    terminalLog.scrollTop = terminalLog.scrollHeight;
  }

  function ensureAudioContext() {
    if (audioContext) return audioContext;
    const Ctx = window.AudioContext || window.webkitAudioContext;
    if (!Ctx) return null;
    audioContext = new Ctx();
    return audioContext;
  }

  async function unlockAudioContext() {
    const ctx = ensureAudioContext();
    if (!ctx) return;
    if (ctx.state === "suspended") {
      try {
        await ctx.resume();
      } catch (_) {
        return;
      }
    }
  }

  function playCompletionChime(taskId) {
    if (!completionChimeInput.checked) return;
    if (chimePlayedTaskId === taskId) return;
    const ctx = ensureAudioContext();
    if (!ctx || ctx.state !== "running") return;
    const schedule = [
      { freq: 880, start: 0, duration: 0.12 },
      { freq: 1174, start: 0.14, duration: 0.12 },
      { freq: 1568, start: 0.28, duration: 0.2 },
    ];
    const volume = 0.12;
    const begin = ctx.currentTime;
    schedule.forEach((note) => {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = "sine";
      osc.frequency.value = note.freq;
      gain.gain.setValueAtTime(0.0001, begin + note.start);
      gain.gain.exponentialRampToValueAtTime(volume, begin + note.start + 0.01);
      gain.gain.exponentialRampToValueAtTime(
        0.0001,
        begin + note.start + note.duration
      );
      osc.connect(gain);
      gain.connect(ctx.destination);
      osc.start(begin + note.start);
      osc.stop(begin + note.start + note.duration + 0.02);
    });
    chimePlayedTaskId = taskId;
  }

  function formatHHMMSS(seconds) {
    const safe = Math.max(
      0,
      Number.isFinite(seconds) ? Math.floor(seconds) : 0
    );
    const h = String(Math.floor(safe / 3600)).padStart(2, "0");
    const m = String(Math.floor((safe % 3600) / 60)).padStart(2, "0");
    const s = String(safe % 60).padStart(2, "0");
    return `${h}:${m}:${s}`;
  }

  function updateTimeStats(data) {
    const elapsedText = document.getElementById("elapsedText");
    const etaText = document.getElementById("etaText");
    const elapsedSec = Number(data.elapsed_sec);
    elapsedText.textContent = `已用时 ${formatHHMMSS(elapsedSec)}`;
    if (data.status === "completed") {
      etaText.textContent = "预计剩余 00:00:00";
      return;
    }
    const etaSec = Number(data.eta_sec);
    if (Number.isFinite(etaSec) && etaSec >= 0) {
      etaText.textContent = `预计剩余 ${formatHHMMSS(etaSec)}`;
      return;
    }
    etaText.textContent = "正在评估剩余时间...";
  }

  // Fetch Explanation Function
  async function fetchExplanation() {
    const config = buildConfigFromForm();

    try {
      const res = await fetch("/api/explain_config", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(config),
      });
      if (res.ok) {
        const data = await res.json();
        // The concurrency recommendation is useful even before a task is
        // open: keep the "auto" option labeled with the live value while
        // the user is in auto mode.
        if (
          data.concurrency !== undefined &&
          document.getElementById("concurrencySelect").value === "0"
        ) {
          updateConcurrencyAutoLabel(data.concurrency);
        }
        // The explanation panel belongs to an open task's dashboard.
        if (currentFile && !dashboard.classList.contains("hidden")) {
          const configExplanation = document.getElementById("configExplanation");
          configExplanation.textContent = data.explanation;
          configExplanation.classList.remove("hidden");
        }
      }
    } catch (e) {
      console.warn("Failed to fetch explanation", e);
    }
  }

  // Build the translation config payload from the sidebar form. Shared by
  // start, resume and config-explain so all three always agree on model,
  // engine and parameters.
  function buildConfigFromForm() {
    const formData = new FormData(configForm);
    const config = Object.fromEntries(formData);

    // Convert glossary text to map
    const glossaryMap = {};
    if (config.glossary_text) {
      const lines = config.glossary_text.split("\n");
      lines.forEach((line) => {
        const parts = line.split("=");
        if (parts.length === 2 && parts[0].trim() && parts[1].trim()) {
          glossaryMap[parts[0].trim()] = parts[1].trim();
        }
      });
    }
    config.glossary = glossaryMap;
    config.bilingual = configForm.querySelector(
      'input[name="bilingual"]'
    ).checked;
    config.chapter_batching = configForm.querySelector(
      'input[name="chapter_batching"]'
    ).checked;
    delete config.glossary_text;

    // Type conversions. Concurrency follows the select: 0 = let the
    // backend auto-plan (RAM/model aware), 1-4 = user-pinned.
    config.concurrency = parseInt(config.concurrency || "0", 10) || 0;
    config.max_chunk_size = parseInt(config.max_chunk_size || "0", 10) || 0;
    config.request_timeout_sec = parseInt(config.request_timeout_sec);
    config.max_retries = parseInt(config.max_retries);
    return config;
  }

  configForm.addEventListener("change", () => {
    // Debounced probe: refreshes the concurrency recommendation label at
    // all times and the dashboard explanation when a task is open.
    scheduleExplain();
  });

  // File Handling
  fileInput.addEventListener("change", (e) => {
    if (e.target.files.length > 0) {
      currentFile = e.target.files[0];
      // Capture pre-reset state: eventSource is only non-null while a task
      // is actively streaming (completed/paused handlers null it out), so
      // the toast below never claims a finished task is "still running".
      const wasStreaming = !!eventSource;
      const hadPreviousTask = !!currentTaskId;
      resetDashboardForNewTask();
      selectedFileName.textContent = `已选择: ${currentFile.name} (${(currentFile.size / 1024 / 1024).toFixed(2)} MB)`;
      uploadZone.style.borderColor = "var(--success)";
      dashboard.classList.remove("hidden");
      document.getElementById("taskTitle").textContent =
        `准备翻译: ${currentFile.name}`;

      // Layout Shift
      topBar.style.display = "none";
      uploadZone.style.padding = "30px";

      log(`已选择文件 ${currentFile.name}，点击"开始执行翻译"处理该文档`, "gray");
      if (wasStreaming) {
        showToast(
          "已切换到新文件；原任务仍在后台翻译，可在历史记录中查看",
          "info"
        );
        fetchTasks();
      } else if (hadPreviousTask) {
        showToast(
          "已切换到新文件；原任务已保留，可在历史记录中查看或继续",
          "info"
        );
      }

      fetchExplanation();

      // Clear the input so re-picking the same file still fires "change".
      fileInput.value = "";
    }
  });

  // Drag and Drop
  uploadZone.addEventListener("dragover", (e) => {
    e.preventDefault();
    uploadZone.style.borderColor = "var(--accent)";
  });
  uploadZone.addEventListener("dragleave", (e) => {
    e.preventDefault();
    uploadZone.style.borderColor = currentFile
      ? "var(--success)"
      : "var(--border)";
  });
  uploadZone.addEventListener("drop", (e) => {
    e.preventDefault();
    if (e.dataTransfer.files.length > 0) {
      fileInput.files = e.dataTransfer.files;
      fileInput.dispatchEvent(new Event("change"));
    }
  });

  // Start Translation
  startBtn.addEventListener("click", async () => {
    if (!currentFile) return;
    await unlockAudioContext();

    // Parse Form
    const config = buildConfigFromForm();

    // Prep API Call
    const apiFormData = new FormData();
    apiFormData.append("file", currentFile);
    apiFormData.append("config", JSON.stringify(config));

    saveHistory(config);

    startBtn.disabled = true;
    startBtn.textContent = "翻译中...";
    chimePlayedTaskId = "";
    document.getElementById("statusBadge").textContent = "执行中";
    document.getElementById("statusBadge").style.backgroundColor =
      "rgba(248, 81, 73, 0.1)";
    document.getElementById("statusBadge").style.color = "var(--text-red)";
    document.getElementById("elapsedText").textContent = "已用时 00:00:00";
    document.getElementById("etaText").textContent = "预计剩余 计算中...";

    const rBtn = document.getElementById("resumeBtn");
    if (rBtn) rBtn.classList.add("hidden");

    document.getElementById("statsDashboard").classList.add("hidden");
    document.getElementById("downloadFailuresBtn").classList.add("hidden");
    downloadBtn.classList.add("hidden");

    log("正在上传文档并初始化引擎参数...", "gray");

    try {
      const response = await fetch("/api/translate", {
        method: "POST",
        body: apiFormData,
      });

      if (!response.ok) {
        const errText = await response.text();
        throw new Error(errText);
      }

      const { task_id } = await response.json();
      currentTaskId = task_id;
      log(`成功分配任务 ID: ${task_id}`, "green");
      log(`开始建立长连接实时监听翻译进度...`, "gray");

      // Establish SSE Connection for real-time progress
      listenToProgress(task_id);
      if (pauseBtn) {
        pauseBtn.classList.remove("hidden");
        pauseBtn.onclick = () => pauseTask(task_id);
      }
      if (stopExportBtn) {
        stopExportBtn.classList.remove("hidden");
        stopExportBtn.onclick = () => stopExportTask(task_id);
      }
    } catch (error) {
      console.error(error);
      showToast(error.message, "error");
      log(`引擎启动失败: ${error.message}`, "red");
      resetUI("启动失败");
    }
  });

  function listenToProgress(taskId) {
    if (eventSource) eventSource.close();
    if (heartbeatInterval) clearInterval(heartbeatInterval);

    eventSource = new EventSource(`/api/progress?task_id=${taskId}`);
    let lastHeartbeat = Date.now();

    heartbeatInterval = setInterval(() => {
      if (Date.now() - lastHeartbeat > 15000) {
        console.warn("Heartbeat timeout, disconnecting...");
        clearInterval(heartbeatInterval);
        if (eventSource) eventSource.close();
        handleDisconnect(taskId);
      }
    }, 5000);

    eventSource.onmessage = (e) => {
      lastHeartbeat = Date.now(); // Any message counts as a connection indicator
      const data = JSON.parse(e.data);

      if (data.type === "heartbeat") {
        return;
      }

      // Update Progress Bar
      if (data.total > 0) {
        const percent = Math.round((data.current / data.total) * 100);
        document.getElementById("progressFill").style.width = `${percent}%`;
        document.getElementById("progressPercent").textContent = `${percent}%`;
        document.getElementById("progressText").textContent =
          `${data.current} / ${data.total} 块`;
      }
      updateTimeStats(data);

      // Append Log Message
      if (data.message) {
        log(data.message, data.type || "gray");
      }

      // Handle Completion
      if (data.status === "completed") {
        clearInterval(heartbeatInterval);
        heartbeatInterval = null;
        eventSource.close();
        eventSource = null;
        log("🎉 翻译任务圆满完成！", "green");
        showToast("翻译完成，可以下载了！", "success");
        playCompletionChime(taskId);

        document.getElementById("statusBadge").textContent = "已完成";
        document.getElementById("statusBadge").style.backgroundColor =
          "rgba(35, 134, 54, 0.1)";
        document.getElementById("statusBadge").style.color = "var(--success)";

        startBtn.classList.add("hidden");
        downloadBtn.classList.remove("hidden");
        if (pauseBtn) pauseBtn.classList.add("hidden");
        if (stopExportBtn) stopExportBtn.classList.add("hidden");

        downloadBtn.onclick = () => {
          window.location.href = `/api/download?task_id=${taskId}`;
        };

        // Fetch Stats
        fetch(`/api/task_status?task_id=${taskId}`)
          .then((res) => res.json())
          .then((taskData) => {
            // The user may have switched to another file while this request
            // was in flight; never leak the old task's stats into the new
            // dashboard context.
            if (currentTaskId !== taskId) return;
            if (taskData.status_reason === "stopped_partial") {
              document.getElementById("statusBadge").textContent = "部分完成";
              showToast("任务已终止并导出部分译本", "info");
            }
            if (taskData.stats) {
              document
                .getElementById("statsDashboard")
                .classList.remove("hidden");
              document.getElementById("statSuccess").textContent =
                `成功: ${taskData.stats.success_count || 0}`;
              document.getElementById("statFallback").textContent =
                `降级: ${taskData.stats.fallback_count || 0}`;
              document.getElementById("statRefused").textContent =
                `拒答: ${taskData.stats.refused_count || 0}`;
              document.getElementById("statFailed").textContent =
                `失败: ${taskData.stats.failure_count || 0}`;

              if (
                taskData.stats.failed_blocks &&
                taskData.stats.failed_blocks.length > 0
              ) {
                const dfBtn = document.getElementById("downloadFailuresBtn");
                dfBtn.classList.remove("hidden");
                dfBtn.textContent = `下载失败记录 (${taskData.stats.failed_blocks.length})`;
                dfBtn.onclick = () => {
                  window.location.href = `/api/download_failures?task_id=${taskId}`;
                };
              }
            }
          })
          .catch((e) => console.error("Failed to fetch stats", e));
      }

      // Handle Error
      if (data.status === "error") {
        clearInterval(heartbeatInterval);
        heartbeatInterval = null;
        eventSource.close();
        eventSource = null;
        showToast("翻译过程中断", "error");
        handleDisconnect(taskId);
      }

      if (data.status === "paused") {
        clearInterval(heartbeatInterval);
        heartbeatInterval = null;
        eventSource.close();
        eventSource = null;
        showToast("任务已暂停，可点击“继续任务”恢复翻译", "info");
        const badge = document.getElementById("statusBadge");
        badge.textContent = "已暂停";
        badge.style.backgroundColor = "rgba(210, 153, 34, 0.1)";
        badge.style.color = "#d29922";
        if (pauseBtn) pauseBtn.classList.add("hidden");
        if (stopExportBtn) stopExportBtn.classList.add("hidden");
        // Keep an in-place resume affordance instead of dead-ending the
        // user into the history modal.
        showResumeButton(taskId, "继续任务");
        fetchTasks();
      }
    };

    eventSource.onerror = (e) => {
      console.error("SSE Error", e);
      clearInterval(heartbeatInterval);
      heartbeatInterval = null;
      eventSource.close();
      eventSource = null;
      log("连接丢失或无法连接到日志服务器", "orange");
      handleDisconnect(taskId);
    };
  }

  async function handleDisconnect(taskId) {
    if (currentTaskId !== taskId) {
      // The dashboard has moved on to another file/task; leave it untouched.
      fetchTasks();
      return;
    }
    log("连接已断开，尝试获取任务状态...", "orange");
    document.getElementById("statusBadge").textContent = "连接断开";
    document.getElementById("statusBadge").style.backgroundColor =
      "rgba(210, 153, 34, 0.1)";
    document.getElementById("statusBadge").style.color = "#d29922";

    try {
      const res = await fetch(`/api/task_status?task_id=${taskId}`);
      if (res.ok) {
        const data = await res.json();
        if (currentTaskId !== taskId) {
          fetchTasks();
          return;
        }
        if (data.resume_supported) {
          showResumeButton(taskId);
          fetchTasks();
          return;
        }
      }
      fetchTasks();
      resetUI();
    } catch (e) {
      fetchTasks();
      resetUI();
    }
  }

  function showResumeButton(taskId, label = "断点重试") {
    startBtn.classList.add("hidden");
    let rBtn = document.getElementById("resumeBtn");
    if (!rBtn) {
      rBtn = document.createElement("button");
      rBtn.id = "resumeBtn";
      rBtn.className = "btn-primary";
      startBtn.parentNode.appendChild(rBtn);
    }
    // Always rebind so the latest task id and label win, no matter which
    // flow (pause, disconnect) created or last showed the button.
    rBtn.classList.remove("hidden");
    rBtn.disabled = false;
    rBtn.textContent = label;
    rBtn.onclick = async () => {
      rBtn.disabled = true;
      rBtn.textContent = "恢复中...";
      try {
        const config = await resumeTaskById(taskId);

        log(
          `任务已恢复（模型: ${config.model}），正在重新连接进度流...`,
          "green"
        );
        rBtn.classList.add("hidden");
        // The badge already reflects "执行中"; a disabled primary button
        // that says "翻译中..." would just be confusing.
        startBtn.classList.add("hidden");

        document.getElementById("statusBadge").textContent = "执行中";
        document.getElementById("statusBadge").style.backgroundColor =
          "rgba(248, 81, 73, 0.1)";
        document.getElementById("statusBadge").style.color =
          "var(--text-red)";

        listenToProgress(taskId);
        if (pauseBtn) {
          pauseBtn.classList.remove("hidden");
          pauseBtn.onclick = () => pauseTask(taskId);
        }
        if (stopExportBtn) {
          stopExportBtn.classList.remove("hidden");
          stopExportBtn.onclick = () => stopExportTask(taskId);
        }
      } catch (e) {
        showToast("恢复失败: " + e.message, "error");
        rBtn.disabled = false;
        rBtn.textContent = label;
      }
    };
  }

  // Reset every dashboard widget to the "ready for a new task" state. Used
  // when the user picks a new file (or opens another task from history) so
  // nothing from a previous task — download button, stats, progress bar,
  // badges, progress stream — leaks into the new context.
  function resetDashboardForNewTask() {
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
    if (heartbeatInterval) {
      clearInterval(heartbeatInterval);
      heartbeatInterval = null;
    }
    currentTaskId = "";
    chimePlayedTaskId = "";

    startBtn.disabled = false;
    startBtn.textContent = "开始执行翻译";
    startBtn.classList.remove("hidden");
    downloadBtn.classList.add("hidden");
    downloadBtn.onclick = null;
    if (pauseBtn) pauseBtn.classList.add("hidden");
    if (stopExportBtn) stopExportBtn.classList.add("hidden");
    const rBtn = document.getElementById("resumeBtn");
    if (rBtn) rBtn.classList.add("hidden");
    const dfBtn = document.getElementById("downloadFailuresBtn");
    if (dfBtn) dfBtn.classList.add("hidden");

    // Stale per-task panels: the config explanation and terminal log belong
    // to the previous task, and a debounced explain request may be pending.
    clearTimeout(window.explainTimeout);
    document.getElementById("configExplanation").classList.add("hidden");
    terminalLog.innerHTML = "";

    const badge = document.getElementById("statusBadge");
    badge.textContent = "待开始";
    badge.style.backgroundColor = "rgba(139, 148, 158, 0.12)";
    badge.style.color = "var(--text-muted)";

    document.getElementById("statsDashboard").classList.add("hidden");
    document.getElementById("statSuccess").textContent = "成功: 0";
    document.getElementById("statFallback").textContent = "降级: 0";
    document.getElementById("statRefused").textContent = "拒答: 0";
    document.getElementById("statFailed").textContent = "失败: 0";
    document.getElementById("progressFill").style.width = "0%";
    document.getElementById("progressPercent").textContent = "0%";
    document.getElementById("progressText").textContent = "等待开始";
    document.getElementById("elapsedText").textContent = "已用时 00:00:00";
    document.getElementById("etaText").textContent = "预计剩余 --:--:--";
  }

  function resetUI(badgeText = "已中断") {
    startBtn.disabled = false;
    startBtn.textContent = "重新执行";
    startBtn.classList.remove("hidden");
    const rBtn = document.getElementById("resumeBtn");
    if (rBtn) rBtn.classList.add("hidden");
    if (pauseBtn) pauseBtn.classList.add("hidden");
    if (stopExportBtn) stopExportBtn.classList.add("hidden");
    downloadBtn.classList.add("hidden");
    const badge = document.getElementById("statusBadge");
    badge.textContent = badgeText;
    badge.style.backgroundColor = "rgba(248, 81, 73, 0.1)";
    badge.style.color = "var(--text-red)";
    document.getElementById("elapsedText").textContent = "已用时 00:00:00";
    document.getElementById("etaText").textContent = "预计剩余 --:--:--";
  }
});
