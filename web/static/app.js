document.addEventListener('DOMContentLoaded', () => {
    const fileInput = document.getElementById('fileInput');
    const selectedFileName = document.getElementById('selectedFileName');
    const uploadZone = document.getElementById('uploadZone');
    const topBar = document.querySelector('.top-bar');
    const dashboard = document.getElementById('dashboard');
    const startBtn = document.getElementById('startBtn');
    const downloadBtn = document.getElementById('downloadBtn');
    const configForm = document.getElementById('configForm');
    const terminalLog = document.getElementById('terminalLog');

    let currentFile = null;
    let eventSource = null;

    const modelSelect = document.getElementById('modelSelect');
    const modelInput = document.getElementById('modelInput');

    modelSelect.addEventListener('change', (e) => {
        if (e.target.value === '__custom__') {
            modelInput.style.display = 'block';
            modelInput.value = '';
        } else {
            modelInput.style.display = 'none';
            modelInput.value = e.target.value;
        }
    });

    // Fetch Models Function
    async function fetchModels() {
        try {
            const apiUrl = document.querySelector('input[name="api_url"]').value;
            const res = await fetch(`/api/models?api_url=${encodeURIComponent(apiUrl)}`);
            if (res.ok) {
                const data = await res.json();
                if (data.models && data.models.length > 0) {
                    modelSelect.innerHTML = '';
                    data.models.forEach(m => {
                        const opt = document.createElement('option');
                        opt.value = m;
                        opt.textContent = m;
                        modelSelect.appendChild(opt);
                    });

                    const customOpt = document.createElement('option');
                    customOpt.value = '__custom__';
                    customOpt.textContent = '➕ 自定义手动输入...';
                    modelSelect.appendChild(customOpt);

                    // Initialize hidden input
                    modelSelect.value = data.models[0];
                    modelSelect.dispatchEvent(new Event('change'));
                } else {
                    modelSelect.innerHTML = '<option value="__custom__">未检测到模型 (手动输入)</option>';
                    modelSelect.value = '__custom__';
                    modelSelect.dispatchEvent(new Event('change'));
                }
            }
        } catch (e) {
            console.warn("Failed to fetch models", e);
            modelSelect.innerHTML = '<option value="__custom__">无法连接Ollama (手动输入)</option>';
            modelSelect.value = '__custom__';
            modelSelect.dispatchEvent(new Event('change'));
        }
    }

    // Initialize models list
    fetchModels();
    document.querySelector('input[name="api_url"]').addEventListener('blur', fetchModels);

    // Toast Notification System
    function showToast(message, type = 'info') {
        const container = document.getElementById('toastContainer');
        const toast = document.createElement('div');
        toast.className = `toast ${type}`;
        toast.textContent = message;
        container.appendChild(toast);
        setTimeout(() => toast.remove(), 4000);
    }

    // Terminal Logger
    function log(message, type = 'gray') {
        const line = document.createElement('div');
        line.className = `log-line text-${type}`;
        const time = new Date().toLocaleTimeString('en-US', { hour12: false });
        line.textContent = `[${time}] ${message}`;
        terminalLog.appendChild(line);
        terminalLog.scrollTop = terminalLog.scrollHeight;
    }

    // File Handling
    fileInput.addEventListener('change', (e) => {
        if (e.target.files.length > 0) {
            currentFile = e.target.files[0];
            selectedFileName.textContent = `已选择: ${currentFile.name} (${(currentFile.size / 1024 / 1024).toFixed(2)} MB)`;
            uploadZone.style.borderColor = 'var(--success)';
            dashboard.classList.remove('hidden');
            document.getElementById('taskTitle').textContent = `准备翻译: ${currentFile.name}`;

            // Layout Shift
            topBar.style.display = 'none';
            uploadZone.style.padding = '30px';
        }
    });

    // Drag and Drop
    uploadZone.addEventListener('dragover', (e) => {
        e.preventDefault();
        uploadZone.style.borderColor = 'var(--accent)';
    });
    uploadZone.addEventListener('dragleave', (e) => {
        e.preventDefault();
        uploadZone.style.borderColor = currentFile ? 'var(--success)' : 'var(--border)';
    });
    uploadZone.addEventListener('drop', (e) => {
        e.preventDefault();
        if (e.dataTransfer.files.length > 0) {
            fileInput.files = e.dataTransfer.files;
            fileInput.dispatchEvent(new Event('change'));
        }
    });

    // Start Translation
    startBtn.addEventListener('click', async () => {
        if (!currentFile) return;

        // Parse Form
        const formData = new FormData(configForm);
        const config = Object.fromEntries(formData);

        // Convert glossary text to map
        const glossaryMap = {};
        if (config.glossary_text) {
            const lines = config.glossary_text.split('\n');
            lines.forEach(line => {
                const parts = line.split('=');
                if (parts.length === 2 && parts[0].trim() && parts[1].trim()) {
                    glossaryMap[parts[0].trim()] = parts[1].trim();
                }
            });
        }
        config.glossary = glossaryMap;
        config.bilingual = configForm.querySelector('input[name="bilingual"]').checked;
        delete config.glossary_text;

        // Type conversions
        config.concurrency = parseInt(config.concurrency);
        config.max_chunk_size = parseInt(config.max_chunk_size);
        config.request_timeout_sec = parseInt(config.request_timeout_sec);

        // Prep API Call
        const apiFormData = new FormData();
        apiFormData.append('file', currentFile);
        apiFormData.append('config', JSON.stringify(config));

        startBtn.disabled = true;
        startBtn.textContent = '翻译中...';
        document.getElementById('statusBadge').textContent = '执行中';
        document.getElementById('statusBadge').style.backgroundColor = 'rgba(248, 81, 73, 0.1)';
        document.getElementById('statusBadge').style.color = 'var(--text-red)';

        log('正在上传文档并初始化引擎参数...', 'gray');

        try {
            const response = await fetch('/api/translate', {
                method: 'POST',
                body: apiFormData
            });

            if (!response.ok) {
                const errText = await response.text();
                throw new Error(errText);
            }

            const { task_id } = await response.json();
            log(`成功分配任务 ID: ${task_id}`, 'green');
            log(`开始建立长连接实时监听翻译进度...`, 'gray');

            // Establish SSE Connection for real-time progress
            listenToProgress(task_id);

        } catch (error) {
            console.error(error);
            showToast(error.message, 'error');
            log(`引擎启动失败: ${error.message}`, 'red');
            resetUI();
        }
    });

    function listenToProgress(taskId) {
        if (eventSource) eventSource.close();

        eventSource = new EventSource(`/api/progress?task_id=${taskId}`);

        eventSource.onmessage = (e) => {
            const data = JSON.parse(e.data);

            // Update Progress Bar
            if (data.total > 0) {
                const percent = Math.round((data.current / data.total) * 100);
                document.getElementById('progressFill').style.width = `${percent}%`;
                document.getElementById('progressPercent').textContent = `${percent}%`;
                document.getElementById('progressText').textContent = `${data.current} / ${data.total} 块`;
            }

            // Append Log Message
            if (data.message) {
                log(data.message, data.type || 'gray');
            }

            // Handle Completion
            if (data.status === 'completed') {
                eventSource.close();
                log('🎉 翻译任务圆满完成！', 'green');
                showToast('翻译完成，可以下载了！', 'success');

                document.getElementById('statusBadge').textContent = '已完成';
                document.getElementById('statusBadge').style.backgroundColor = 'rgba(35, 134, 54, 0.1)';
                document.getElementById('statusBadge').style.color = 'var(--success)';

                startBtn.classList.add('hidden');
                downloadBtn.classList.remove('hidden');

                downloadBtn.onclick = () => {
                    window.location.href = `/api/download?task_id=${taskId}`;
                };
            }

            // Handle Error
            if (data.status === 'error') {
                eventSource.close();
                showToast('翻译过程中断', 'error');
                resetUI();
            }
        };

        eventSource.onerror = (e) => {
            console.error("SSE Error", e);
            eventSource.close();
            log('无法连接到日志服务器', 'red');
            resetUI();
        };
    }

    function resetUI() {
        startBtn.disabled = false;
        startBtn.textContent = '重新执行';
        document.getElementById('statusBadge').textContent = '已中断';
    }
});
