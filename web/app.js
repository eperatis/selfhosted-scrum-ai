const NOTIFICATIONS_STORAGE_KEY = 'scrum_ai_notifications_v1';
const THEME_STORAGE_KEY = 'scrum_ai_theme';
const BOARD_REFRESH_INTERVAL_MS = 5000;

const COLUMNS = [
    { id: 'backlog', name: 'Backlog', color: 'var(--color-status-backlog)' },
    { id: 'todo', name: 'To Do', color: 'var(--color-status-todo)' },
    { id: 'inprogress', name: 'In Progress', color: 'var(--color-status-progress)' },
    { id: 'review', name: 'Review', color: 'var(--color-status-review)' },
    { id: 'done', name: 'Done', color: 'var(--color-status-done)' }
];

let boardFileData = { boards: [] };
let currentBoard = { tasks: [] };
let notifications = JSON.parse(localStorage.getItem(NOTIFICATIONS_STORAGE_KEY)) || [];
let draggedTaskId = null;
let blockerIdsInTaskModal = [];
// Version of board.json this tab last loaded or saved, so the server can refuse a save over newer changes
let boardFileETag = null;
// Server calls run one at a time, so a save always sends the ETag the previous one got back
let serverRequestQueue = Promise.resolve();

document.addEventListener('DOMContentLoaded', () => {
    applyTheme(localStorage.getItem(THEME_STORAGE_KEY) || 'light');
    showLLMAPIAddress();
    renderColumns();
    renderNotifications();

    document.addEventListener('pointerdown', event => {
        if (!event.target.closest('.notifications')) closeNotifications();
    });

    // The URL hash holds the board id, so each tab can show a different board
    window.addEventListener('hashchange', () => selectBoard(location.hash.slice(1)));

    loadBoardsFromServer()
        .then(() => selectBoard(location.hash.slice(1)))
        .catch(error => notify(`Loading failed: ${error.message}`));

    // Pick up boards and tickets the LLM API (or another tab) created, but not while the user is mid-edit
    setInterval(() => {
        const isUserEditing = draggedTaskId || document.querySelector('.modal:not(.hidden)');
        if (!document.hidden && !isUserEditing) reloadBoardsIfChangedOnServer();
    }, BOARD_REFRESH_INTERVAL_MS);
});

// Storage

async function loadBoardsFromServer() {
    const response = await fetch('/api/board');
    if (!response.ok) throw new Error(`server answered ${response.status} ${await response.text()}`);
    boardFileETag = response.headers.get('ETag');

    // First run: the server has no board.json yet
    const isFirstRun = response.status === 204;
    boardFileData = isFirstRun
        ? { boards: [{ id: 'board-main', name: 'My Board', tasks: [] }] }
        : await response.json();
    currentBoard = boardFileData.boards[0];
    if (isFirstRun) saveBoardsAndRender();
}

function saveBoardsAndRender() {
    renderCurrentBoard();
    serverRequestQueue = serverRequestQueue.then(putBoardsToServer);
}

async function putBoardsToServer() {
    const headers = { 'Content-Type': 'application/json' };
    if (boardFileETag) headers['If-Match'] = boardFileETag;
    try {
        const response = await fetch('/api/board', { method: 'PUT', headers, body: JSON.stringify(boardFileData, null, 2) });
        // board.json changed since this tab loaded it: saving would delete those changes
        if (response.status === 412) {
            await reloadBoardsKeepingSelection();
            notify('Your last change was not saved: the board was changed by the LLM API or another tab, so it was reloaded');
            return;
        }
        if (!response.ok) throw new Error(`server answered ${response.status} ${await response.text()}`);
        boardFileETag = response.headers.get('ETag');
    } catch (error) {
        notify(`Saving failed: ${error.message}`);
    }
}

function reloadBoardsIfChangedOnServer() {
    serverRequestQueue = serverRequestQueue.then(async () => {
        try {
            const response = await fetch('/api/board', { method: 'HEAD' });
            if (!response.ok) throw new Error(`server answered ${response.status}`);
            if (response.headers.get('ETag') !== boardFileETag) await reloadBoardsKeepingSelection();
        } catch (error) {
            // Runs every few seconds, so log instead of filling the notification list
            console.error(`Board refresh failed, retrying in ${BOARD_REFRESH_INTERVAL_MS / 1000}s:`, error);
        }
    });
}

async function reloadBoardsKeepingSelection() {
    const selectedBoardId = currentBoard.id;
    await loadBoardsFromServer();
    selectBoard(selectedBoardId);
}

// Helpers

function newId(prefix) {
    return `${prefix}-${Date.now().toString(36)}`;
}

function today() {
    return new Date().toISOString().split('T')[0];
}

function findTask(taskId) {
    return currentBoard.tasks.find(task => task.id === taskId);
}

function isTaskBlocked(task) {
    return task.blockedBy.some(blockerId => {
        const blockerTask = findTask(blockerId);
        return blockerTask && blockerTask.column !== 'done';
    });
}

function isOverdue(dueDate) {
    if (!dueDate) return false;
    const startOfToday = new Date();
    startOfToday.setHours(0, 0, 0, 0);
    return new Date(dueDate) < startOfToday;
}

function escapeHtml(text) {
    if (!text) return '';
    return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// Boards

function findBoard(boardId) {
    return boardFileData.boards.find(board => board.id === boardId);
}

function selectBoard(boardId) {
    currentBoard = findBoard(boardId) || boardFileData.boards[0];
    if (location.hash.slice(1) !== currentBoard.id) history.replaceState(null, '', `#${currentBoard.id}`);
    renderBoardSelect();
    renderCurrentBoard();
}

function renderBoardSelect() {
    const boardSelect = document.getElementById('boardSelect');
    boardSelect.innerHTML = boardFileData.boards
        .map(board => `<option value="${board.id}">${escapeHtml(board.name)}</option>`)
        .join('');
    boardSelect.value = currentBoard.id;
}

function addBoard(boardName) {
    const newBoard = { id: newId('board'), name: boardName, tasks: [] };
    boardFileData.boards.push(newBoard);
    selectBoard(newBoard.id);
    saveBoardsAndRender();
}

function openBoardModal(isNewBoard) {
    document.getElementById('modalBoardTitle').innerText = isNewBoard ? 'New Board' : 'Edit Board';
    document.getElementById('boardIsNew').value = isNewBoard ? '1' : '';
    document.getElementById('boardName').value = isNewBoard ? '' : currentBoard.name;
    const isLastBoard = boardFileData.boards.length < 2;
    document.getElementById('btnDeleteBoard').classList.toggle('hidden', isNewBoard || isLastBoard);
    openModal('boardModal');
    document.getElementById('boardName').focus();
}

function saveBoardForm(event) {
    event.preventDefault();
    const boardName = document.getElementById('boardName').value.trim();
    if (document.getElementById('boardIsNew').value) {
        addBoard(boardName);
        notify(`Board created: ${boardName}`);
    } else {
        currentBoard.name = boardName;
        renderBoardSelect();
        saveBoardsAndRender();
        notify(`Board renamed: ${boardName}`);
    }
    closeModal('boardModal');
}

function deleteCurrentBoard() {
    const ticketCount = currentBoard.tasks.length;
    if (ticketCount && !confirm(`Delete "${currentBoard.name}" and its ${ticketCount} ticket${ticketCount > 1 ? 's' : ''}?`)) return;
    const deletedBoardName = currentBoard.name;
    boardFileData.boards = boardFileData.boards.filter(board => board !== currentBoard);
    selectBoard(boardFileData.boards[0].id);
    saveBoardsAndRender();
    closeModal('boardModal');
    notify(`Board deleted: ${deletedBoardName}`);
}

// Board rendering

function renderColumns() {
    document.getElementById('board').innerHTML = COLUMNS.map(column => `
        <div class="column">
            <div class="column-header">
                <h2><span class="dot" style="background: ${column.color}"></span>${column.name}</h2>
                <span id="count-${column.id}" class="count">0</span>
            </div>
            <div id="col-${column.id}" ondragover="handleDragOver(event)" ondragleave="handleDragLeave(event)" ondrop="handleDrop(event, '${column.id}')" class="column-body"></div>
        </div>
    `).join('');

    document.getElementById('taskColumn').innerHTML = COLUMNS
        .map(column => `<option value="${column.id}">${column.name}</option>`)
        .join('');
}

function renderCurrentBoard() {
    const searchText = document.getElementById('searchInput').value.toLowerCase().trim();
    const matchesSearch = task => !searchText
        || task.title.toLowerCase().includes(searchText)
        || task.desc.toLowerCase().includes(searchText);

    COLUMNS.forEach(column => {
        const columnTasks = currentBoard.tasks.filter(task => task.column === column.id && matchesSearch(task));
        const columnBody = document.getElementById(`col-${column.id}`);
        columnBody.innerHTML = '';
        columnTasks.forEach(task => columnBody.appendChild(createCard(task)));
        document.getElementById(`count-${column.id}`).innerText = columnTasks.length;
    });
}

function createCard(task) {
    const card = document.createElement('div');
    card.className = 'card';
    card.draggable = true;

    card.addEventListener('dragstart', event => {
        draggedTaskId = task.id;
        card.classList.add('dragging');
        event.dataTransfer.setData('text/plain', task.id);
    });
    card.addEventListener('dragend', () => {
        card.classList.remove('dragging');
        draggedTaskId = null;
    });
    card.addEventListener('click', () => openTaskModal(task.id));

    const columnIndex = COLUMNS.findIndex(column => column.id === task.column);
    const previousColumn = COLUMNS[columnIndex - 1];
    const nextColumn = COLUMNS[columnIndex + 1];
    const isOverdueAndNotDone = isOverdue(task.dueDate) && task.column !== 'done';
    const blockerTitles = task.blockedBy.map(blockerId => findTask(blockerId)?.title).filter(Boolean).join(', ');

    card.innerHTML = `
        ${isTaskBlocked(task) ? `
            <span class="badge-blocked" title="Blocked by: ${escapeHtml(blockerTitles)}">
                Blocked
            </span>` : ''}

        <h3 class="card-title">${escapeHtml(task.title)}</h3>
        ${task.desc ? `<p class="card-desc">${escapeHtml(task.desc)}</p>` : ''}

        <div class="card-footer">
            <div class="card-meta">
                ${task.dueDate ? `<span class="${isOverdueAndNotDone ? 'overdue' : ''}">Due ${task.dueDate.slice(5)}</span>` : ''}
                ${task.comments.length ? `<span>${task.comments.length} comment${task.comments.length > 1 ? 's' : ''}</span>` : ''}
            </div>
            <div class="card-moves">
                ${previousColumn ? `<button onclick="event.stopPropagation(); moveTask('${task.id}', '${previousColumn.id}')" class="btn btn-secondary btn-sm" title="Move to ${previousColumn.name}">&larr;</button>` : ''}
                ${nextColumn ? `<button onclick="event.stopPropagation(); moveTask('${task.id}', '${nextColumn.id}')" class="btn btn-secondary btn-sm" title="Move to ${nextColumn.name}">&rarr;</button>` : ''}
            </div>
        </div>
    `;

    return card;
}

// Drag & drop

function handleDragOver(event) {
    event.preventDefault();
    event.currentTarget.classList.add('drag-over');
}

function handleDragLeave(event) {
    event.currentTarget.classList.remove('drag-over');
}

function handleDrop(event, targetColumnId) {
    event.preventDefault();
    event.currentTarget.classList.remove('drag-over');
    const droppedTaskId = event.dataTransfer.getData('text/plain') || draggedTaskId;
    if (droppedTaskId) moveTask(droppedTaskId, targetColumnId);
}

// Task actions

function moveTask(taskId, targetColumnId) {
    const task = findTask(taskId);
    if (!task) throw new Error(`moveTask: no ticket ${taskId} on board ${currentBoard.id}`);
    task.column = targetColumnId;
    saveBoardsAndRender();
}

function addTask(taskFields) {
    currentBoard.tasks.push({ id: newId('task'), ...taskFields, createdAt: today(), comments: [] });
    saveBoardsAndRender();
}

function addComment(task, commentText) {
    task.comments.push({ id: newId('comment'), text: commentText, createdAt: new Date().toISOString() });
    saveBoardsAndRender();
}

// Modals

function openModal(modalId) {
    document.getElementById(modalId).classList.remove('hidden');
}

function closeModal(modalId) {
    document.getElementById(modalId).classList.add('hidden');
}

function openTaskModal(taskId = null) {
    const task = taskId ? findTask(taskId) : null;
    document.getElementById('taskForm').reset();

    document.getElementById('modalTaskTitle').innerText = task ? 'Edit Ticket' : 'New Ticket';
    document.getElementById('taskId').value = task ? task.id : '';
    document.getElementById('btnDeleteTask').classList.toggle('hidden', !task);
    document.getElementById('commentsSection').classList.toggle('hidden', !task);
    document.getElementById('commentInput').value = '';

    if (task) {
        document.getElementById('taskTitle').value = task.title;
        document.getElementById('taskDesc').value = task.desc;
        document.getElementById('taskColumn').value = task.column;
        document.getElementById('taskDueDate').value = task.dueDate;
        renderComments(task);
    }

    blockerIdsInTaskModal = task ? [...task.blockedBy] : [];
    renderBlockers();
    openModal('taskModal');
}

function saveTaskForm(event) {
    event.preventDefault();
    const taskFields = {
        title: document.getElementById('taskTitle').value.trim(),
        desc: document.getElementById('taskDesc').value.trim(),
        column: document.getElementById('taskColumn').value,
        dueDate: document.getElementById('taskDueDate').value,
        blockedBy: blockerIdsInTaskModal
    };

    const existingTask = findTask(document.getElementById('taskId').value);
    if (existingTask) {
        Object.assign(existingTask, taskFields);
        saveBoardsAndRender();
        notify(`Ticket updated: ${existingTask.title}`);
    } else {
        addTask(taskFields);
        notify(`Ticket created: ${taskFields.title}`);
    }
    closeModal('taskModal');
}

function deleteCurrentTask() {
    const deletedTaskId = document.getElementById('taskId').value;
    const deletedTaskTitle = findTask(deletedTaskId).title;
    currentBoard.tasks = currentBoard.tasks.filter(task => task.id !== deletedTaskId);
    currentBoard.tasks.forEach(task => {
        task.blockedBy = task.blockedBy.filter(blockerId => blockerId !== deletedTaskId);
    });
    saveBoardsAndRender();
    closeModal('taskModal');
    notify(`Ticket deleted: ${deletedTaskTitle}`);
}

// Blockers (kept in blockerIdsInTaskModal until the form is saved)

function renderBlockers() {
    const editedTaskId = document.getElementById('taskId').value;

    document.getElementById('blockerList').innerHTML = blockerIdsInTaskModal.map(blockerId => {
        const blockerTask = findTask(blockerId);
        if (!blockerTask) return '';
        return `
            <li class="list-item">
                <span class="${blockerTask.column === 'done' ? 'done-text' : ''}">${escapeHtml(blockerTask.title)}</span>
                <button type="button" onclick="removeBlockerFromTaskModal('${blockerId}')" class="icon-btn">
                    &times;
                </button>
            </li>
        `;
    }).join('');

    const selectableBlockerTasks = currentBoard.tasks.filter(task => task.id !== editedTaskId && !blockerIdsInTaskModal.includes(task.id));
    document.getElementById('blockerSelect').innerHTML =
        '<option value="">+ Add blocking ticket...</option>' +
        selectableBlockerTasks.map(task => `<option value="${task.id}">${escapeHtml(task.title)}</option>`).join('');
}

function addBlockerToTaskModal(blockerId) {
    if (!blockerId) return;
    blockerIdsInTaskModal.push(blockerId);
    renderBlockers();
}

function removeBlockerFromTaskModal(blockerId) {
    blockerIdsInTaskModal = blockerIdsInTaskModal.filter(id => id !== blockerId);
    renderBlockers();
}

// Comments

function renderComments(task) {
    const commentList = document.getElementById('commentList');
    if (!task.comments.length) {
        commentList.innerHTML = '<li class="empty">No comments yet.</li>';
        return;
    }
    commentList.innerHTML = task.comments.map(comment => `
        <li class="list-item stacked">
            <div class="item-date">${new Date(comment.createdAt).toLocaleString()}</div>
            <p class="item-text">${escapeHtml(comment.text)}</p>
        </li>
    `).join('');
}

function addCommentFromInput() {
    const commentInput = document.getElementById('commentInput');
    const commentText = commentInput.value.trim();
    if (!commentText) return;
    const task = findTask(document.getElementById('taskId').value);
    addComment(task, commentText);
    commentInput.value = '';
    renderComments(task);
}

// Export

async function copyCurrentBoardAsJson() {
    const currentBoardJson = JSON.stringify({
        board: { id: currentBoard.id, name: currentBoard.name },
        columns: COLUMNS.map(column => column.id),
        tasks: currentBoard.tasks.map(task => ({ ...task, blocked: isTaskBlocked(task) }))
    }, null, 2);

    // navigator.clipboard is only available on https / localhost
    if (navigator.clipboard && window.isSecureContext) {
        try {
            await navigator.clipboard.writeText(currentBoardJson);
        } catch (error) {
            notify(`Copying failed: ${error.message}`);
            return;
        }
    } else {
        const hiddenTextArea = document.createElement('textarea');
        hiddenTextArea.value = currentBoardJson;
        document.body.appendChild(hiddenTextArea);
        hiddenTextArea.select();
        const wasCopied = document.execCommand('copy');
        document.body.removeChild(hiddenTextArea);
        if (!wasCopied) {
            notify('Copying failed: the browser blocked clipboard access');
            return;
        }
    }
    notify('Board copied as JSON');
}

// LLM API runs on its own port, so the address shown is this host with that port
async function showLLMAPIAddress() {
    const llmAddressElement = document.getElementById('llmAddress');
    try {
        const response = await fetch('/api/llm-port');
        if (!response.ok) throw new Error(`server answered ${response.status}`);
        const llmAPIPort = await response.json();
        llmAddressElement.innerText = llmAPIPort
            ? `${location.protocol}//${location.hostname}:${llmAPIPort}`
            : 'off: start the server with -llm to turn it on';
    } catch (error) {
        llmAddressElement.innerText = `unknown (${error.message})`;
    }
}

// Theme

function applyTheme(themeName) {
    document.documentElement.dataset.theme = themeName;
    document.getElementById('themeToggle').innerText = themeName === 'dark' ? 'Light' : 'Dark';
}

function toggleTheme() {
    const nextThemeName = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    localStorage.setItem(THEME_STORAGE_KEY, nextThemeName);
    applyTheme(nextThemeName);
}

// Notifications

function notify(message) {
    notifications.unshift({ message, createdAt: new Date().toISOString() });
    saveNotifications();
}

function clearNotifications() {
    notifications = [];
    saveNotifications();
    closeNotifications();
}

function saveNotifications() {
    localStorage.setItem(NOTIFICATIONS_STORAGE_KEY, JSON.stringify(notifications));
    renderNotifications();
}

function renderNotifications() {
    document.getElementById('notificationCount').innerText = notifications.length;
    const notificationList = document.getElementById('notificationList');
    if (!notifications.length) {
        notificationList.innerHTML = '<li class="empty">No notifications.</li>';
        return;
    }
    notificationList.innerHTML = notifications.map(notification => `
        <li class="list-item stacked">
            <div class="item-date">${new Date(notification.createdAt).toLocaleString()}</div>
            <p class="item-text">${escapeHtml(notification.message)}</p>
        </li>
    `).join('');
}

function toggleNotifications() {
    document.getElementById('notificationPanel').classList.toggle('hidden');
}

function closeNotifications() {
    document.getElementById('notificationPanel').classList.add('hidden');
}
