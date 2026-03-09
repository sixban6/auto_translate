document.addEventListener("DOMContentLoaded", () => {
  const fileInput = document.getElementById("fileInput");
  const selectedFileName = document.getElementById("selectedFileName");
  const uploadZone = document.getElementById("uploadZone");
  const topBar = document.querySelector(".top-bar");
  const dashboard = document.getElementById("dashboard");
  const startBtn = document.getElementById("startBtn");
  const downloadBtn = document.getElementById("downloadBtn");
  const pauseBtn = document.getElementById("pauseBtn");
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

  if (historyModal) {
    historyModal.addEventListener("click", (e) => {
      if (e.target === historyModal) {
        closeHistory();
      }
    });
  }

  function saveHistory(config) {
    localStorage.setItem(
      "auto_trans_config",
      JSON.stringify({
        api_url: config.api_url,
        request_timeout_sec: config.request_timeout_sec,
        max_retries: config.max_retries,
        bilingual: config.bilingual,
        completion_chime: completionChimeInput.checked,
      })
    );
  }

  function loadHistory() {
    try {
      const saved = localStorage.getItem("auto_trans_config");
      if (saved) {
        const conf = JSON.parse(saved);
        if (conf.api_url)
          document.querySelector('input[name="api_url"]').value = conf.api_url;
        if (conf.request_timeout_sec)
          document.querySelector('input[name="request_timeout_sec"]').value =
            conf.request_timeout_sec;
        if (conf.max_retries)
          document.querySelector('input[name="max_retries"]').value =
            conf.max_retries;
        if (conf.bilingual !== undefined)
          document.querySelector('input[name="bilingual"]').checked =
            conf.bilingual;
        if (conf.completion_chime !== undefined)
          completionChimeInput.checked = conf.completion_chime;
      }
    } catch (e) {
      console.warn("Failed to load history", e);
    }
  }

  loadHistory();

  const modelSelect = document.getElementById("modelSelect");
  const modelInput = document.getElementById("modelInput");
  const roleSelect = document.getElementById("roleSelect");

  modelSelect.addEventListener("change", (e) => {
    if (e.target.value === "__custom__") {
      modelInput.style.display = "block";
      modelInput.value = "";
    } else {
      modelInput.style.display = "none";
      modelInput.value = e.target.value;
    }
  });

  // Fetch Models Function
  async function fetchModels() {
    try {
      const apiUrl = document.querySelector('input[name="api_url"]').value;
      const res = await fetch(
        `/api/models?api_url=${encodeURIComponent(apiUrl)}`
      );
      if (res.ok) {
        const data = await res.json();
        if (data.models && data.models.length > 0) {
          modelSelect.innerHTML = "";
          data.models.forEach((m) => {
            const opt = document.createElement("option");
            opt.value = m;
            opt.textContent = m;
            modelSelect.appendChild(opt);
          });

          const customOpt = document.createElement("option");
          customOpt.value = "__custom__";
          customOpt.textContent = "➕ 自定义手动输入...";
          modelSelect.appendChild(customOpt);

          // Initialize hidden input
          modelSelect.value = data.models[0];
          modelSelect.dispatchEvent(new Event("change"));
        } else {
          modelSelect.innerHTML =
            '<option value="__custom__">未检测到模型 (手动输入)</option>';
          modelSelect.value = "__custom__";
          modelSelect.dispatchEvent(new Event("change"));
        }
      }
    } catch (e) {
      console.warn("Failed to fetch models", e);
      modelSelect.innerHTML =
        '<option value="__custom__">无法连接Ollama (手动输入)</option>';
      modelSelect.value = "__custom__";
      modelSelect.dispatchEvent(new Event("change"));
    }
  }

  // Initialize models list
  fetchModels();
  document
    .querySelector('input[name="api_url"]')
    .addEventListener("blur", fetchModels);

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

  function statusText(status) {
    if (status === "running") return "进行中";
    if (status === "queued") return "排队中";
    if (status === "interrupted") return "可恢复";
    if (status === "paused") return "已暂停";
    if (status === "completed") return "已完成";
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

    if (!tasks || tasks.length === 0) {
      if (historyEmptyState) historyEmptyState.classList.remove("hidden");
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
      badge.textContent = statusText(displayStatus);
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

      tr.appendChild(nameTd);
      tr.appendChild(statusTd);
      tr.appendChild(progressTd);
      tr.appendChild(actionsTd);

      historyTableBody.appendChild(tr);
    });
  }

  async function deleteTask(taskId) {
    const ok = window.confirm("是否彻底删除该任务及关联文件？进行中的翻译将被中止并清除，此操作不可逆转。");
    if (!ok) return;
    try {
      const res = await fetch(`/api/tasks/${taskId}`, { method: "DELETE" });
      if (!res.ok) throw new Error(await res.text());
      if (currentTaskId === taskId) {
        resetUI();
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
      renderHistoryTable(data.tasks || []);
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

  async function resumeTask(task) {
    try {
      const res = await fetch(`/api/resume?task_id=${task.id}`, {
        method: "POST",
      });
      if (!res.ok) throw new Error(await res.text());
      openTask(task);
      showToast("已开始恢复任务", "success");
    } catch (e) {
      showToast("恢复失败: " + e.message, "error");
    }
  }

  function openTask(task) {
    currentTaskId = task.id;
    dashboard.classList.remove("hidden");
    const displayName =
      task.src_file_name || (task.input_path || "").split("/").pop() || task.id;
    document.getElementById("taskTitle").textContent = `正在翻译: ${displayName}`;
    startBtn.classList.add("hidden");
    downloadBtn.classList.add("hidden");
    if (pauseBtn) {
      pauseBtn.classList.remove("hidden");
      pauseBtn.onclick = () => pauseTask(task.id);
    }
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
    if (!currentFile) return;
    const formData = new FormData(configForm);
    const config = Object.fromEntries(formData);

    // Setup config same as translate
    config.request_timeout_sec = parseInt(config.request_timeout_sec);
    config.max_retries = parseInt(config.max_retries);

    try {
      const res = await fetch("/api/explain_config", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(config),
      });
      if (res.ok) {
        const data = await res.json();
        const configExplanation = document.getElementById("configExplanation");
        configExplanation.textContent = data.explanation;
        configExplanation.classList.remove("hidden");
      }
    } catch (e) {
      console.warn("Failed to fetch explanation", e);
    }
  }

  configForm.addEventListener("change", () => {
    if (!dashboard.classList.contains("hidden")) {
      // Debounced fetch
      clearTimeout(window.explainTimeout);
      window.explainTimeout = setTimeout(fetchExplanation, 500);
    }
  });

  // File Handling
  fileInput.addEventListener("change", (e) => {
    if (e.target.files.length > 0) {
      currentFile = e.target.files[0];
      selectedFileName.textContent = `已选择: ${currentFile.name} (${(currentFile.size / 1024 / 1024).toFixed(2)} MB)`;
      uploadZone.style.borderColor = "var(--success)";
      dashboard.classList.remove("hidden");
      document.getElementById("taskTitle").textContent =
        `准备翻译: ${currentFile.name}`;

      // Layout Shift
      topBar.style.display = "none";
      uploadZone.style.padding = "30px";

      fetchExplanation();
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
    delete config.glossary_text;

    // Type conversions
    config.concurrency = 0;
    config.max_chunk_size = 0;
    config.request_timeout_sec = parseInt(config.request_timeout_sec);
    config.max_retries = parseInt(config.max_retries);

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
    } catch (error) {
      console.error(error);
      showToast(error.message, "error");
      log(`引擎启动失败: ${error.message}`, "red");
      resetUI();
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
        eventSource.close();
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

        downloadBtn.onclick = () => {
          window.location.href = `/api/download?task_id=${taskId}`;
        };

        // Fetch Stats
        fetch(`/api/task_status?task_id=${taskId}`)
          .then((res) => res.json())
          .then((taskData) => {
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
        eventSource.close();
        showToast("翻译过程中断", "error");
        handleDisconnect(taskId);
      }

      if (data.status === "paused") {
        clearInterval(heartbeatInterval);
        eventSource.close();
        showToast("任务已暂停", "info");
        if (pauseBtn) pauseBtn.classList.add("hidden");
        fetchTasks();
      }
    };

    eventSource.onerror = (e) => {
      console.error("SSE Error", e);
      clearInterval(heartbeatInterval);
      eventSource.close();
      log("连接丢失或无法连接到日志服务器", "orange");
      handleDisconnect(taskId);
    };
  }

  async function handleDisconnect(taskId) {
    log("连接已断开，尝试获取任务状态...", "orange");
    document.getElementById("statusBadge").textContent = "连接断开";
    document.getElementById("statusBadge").style.backgroundColor =
      "rgba(210, 153, 34, 0.1)";
    document.getElementById("statusBadge").style.color = "#d29922";

    try {
      const res = await fetch(`/api/task_status?task_id=${taskId}`);
      if (res.ok) {
        const data = await res.json();
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

  function showResumeButton(taskId) {
    startBtn.classList.add("hidden");
    let rBtn = document.getElementById("resumeBtn");
    if (!rBtn) {
      rBtn = document.createElement("button");
      rBtn.id = "resumeBtn";
      rBtn.className = "btn-primary";
      rBtn.textContent = "断点重试 (Resume)";
      rBtn.onclick = async () => {
        rBtn.disabled = true;
        rBtn.textContent = "恢复中...";
        try {
          const res = await fetch(`/api/resume?task_id=${taskId}`, {
            method: "POST",
          });
          if (!res.ok) throw new Error(await res.text());

          log("任务已恢复，正在重新连接进度流...", "green");
          rBtn.classList.add("hidden");
          startBtn.classList.remove("hidden");
          startBtn.disabled = true;
          startBtn.textContent = "翻译中...";

          document.getElementById("statusBadge").textContent = "执行中";
          document.getElementById("statusBadge").style.backgroundColor =
            "rgba(248, 81, 73, 0.1)";
          document.getElementById("statusBadge").style.color =
            "var(--text-red)";

          listenToProgress(taskId);
        } catch (e) {
          showToast("恢复失败: " + e.message, "error");
          rBtn.disabled = false;
          rBtn.textContent = "断点重试 (Resume)";
        }
      };
      startBtn.parentNode.appendChild(rBtn);
    } else {
      rBtn.classList.remove("hidden");
      rBtn.disabled = false;
      rBtn.textContent = "断点重试 (Resume)";
    }
  }

  function resetUI() {
    startBtn.disabled = false;
    startBtn.textContent = "重新执行";
    startBtn.classList.remove("hidden");
    const rBtn = document.getElementById("resumeBtn");
    if (rBtn) rBtn.classList.add("hidden");
    if (pauseBtn) pauseBtn.classList.add("hidden");
    document.getElementById("statusBadge").textContent = "已中断";
    document.getElementById("statusBadge").style.backgroundColor =
      "rgba(248, 81, 73, 0.1)";
    document.getElementById("statusBadge").style.color = "var(--text-red)";
  }
});
