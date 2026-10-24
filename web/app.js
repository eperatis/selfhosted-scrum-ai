const OLD_BOARD_KEY = 'scrum_ai_board_v1';
const NOTIFICATIONS_KEY = 'scrum_ai_notifications_v1';
const THEME_KEY = 'scrum_ai_theme';

const COLUMNS = [
    { id: 'backlog', name: 'Backlog', color: 'var(--color-status-backlog)' },
    { id: 'todo', name: 'To Do', color: 'var(--color-status-todo)' },
    { id: 'inprogress', name: 'In Progress', color: 'var(--color-status-progress)' },
    { id: 'review', name: 'Review', color: 'var(--color-status-review)' },
    { id: 'done', name: 'Done', color: 'var(--color-status-done)' }
];

const defaultBoard = {
    tasks: [
        {
            id: 'task-101',
            title: 'Set up JWT Token Refresh Strategy',
            desc: 'Handle access token expiration and silently refresh tokens in client background.',
            column: 'inprogress',
            dueDate: '2026-10-25',
            blockedBy: [],
            comments: []
        },
        {
            id: 'task-102',
            title: 'Create Automated E2E Tests for Auth Flow',
            desc: 'Write Playwright test cases covering user login, token refresh, and logout.',
            column: 'backlog',
            dueDate: '2026-10-30',
            blockedBy: ['task-101'],
            comments: []
        }
    ]
};

// All boards; `board` is the one currently shown
let data = { boards: [] };
let board = { tasks: [] };
let notifications = JSON.parse(localStorage.getItem(NOTIFICATIONS_KEY)) || [];
let draggedTaskId = null;
let modalBlockers = [];

document.addEventListener('DOMContentLoaded', () => {
    applyTheme(localStorage.getItem(THEME_KEY) || 'light');
    renderColumns();
    renderNotifications();

    // Close the notification panel on any click outside of it
    document.addEventListener('pointerdown', e => {
        if (!e.target.closest('.notifications')) closeNotifications();
    });

    // The URL hash holds the board id, so each tab can show a different board
    window.addEventListener('hashchange', () => selectBoard(location.hash.slice(1)));

    loadBoard()
        .then(() => selectBoard(location.hash.slice(1)))
        .catch(() => notify('Loading failed: could not reach the server'));
});

// Storage

async function loadBoard() {
    const res = await fetch('/api/board');
    const isFirstRun = res.status === 204;

    // First run: the server has no board yet, so upload the old localStorage board (or the demo board)
    const saved = isFirstRun
        ? JSON.parse(localStorage.getItem(OLD_BOARD_KEY)) || defaultBoard
        : await res.json();

    // Files from before multiple boards hold a single { tasks } board
    const isOldFormat = !saved.boards;
    const boards = isOldFormat ? [{ id: 'board-main', name: 'My Board', tasks: saved.tasks }] : saved.boards;

    data = { boards: boards.map(b => ({ id: b.id, name: b.name, tasks: b.tasks.map(normalizeTask) })) };
    board = data.boards[0];
    if (isFirstRun || isOldFormat) saveBoard();
}

// Drops fields from older versions (priority, points, tag) and fills in new ones
function normalizeTask(t) {
    return {
        id: t.id,
        title: t.title,
        desc: t.desc || '',
        column: t.column,
        dueDate: t.dueDate || '',
        createdAt: t.createdAt || t.assignedDate || today(),
        blockedBy: t.blockedBy || [],
        comments: t.comments || []
    };
}

async function saveBoard() {
    renderBoard();
    try {
        const res = await fetch('/api/board', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data, null, 2)
        });
        if (!res.ok) throw new Error(res.status);
    } catch {
        notify('Saving failed: could not reach the server');
    }
}

// Helpers

function newId(prefix) {
    return `${prefix}-${Date.now().toString(36)}`;
}

function today() {
    return new Date().toISOString().split('T')[0];
}

function findTask(id) {
    return board.tasks.find(t => t.id === id);
}

function isBlocked(task) {
    return task.blockedBy.some(id => {
        const blocker = findTask(id);
        return blocker && blocker.column !== 'done';
    });
}

function isOverdue(dueDate) {
    if (!dueDate) return false;
    const now = new Date();
    now.setHours(0, 0, 0, 0);
    return new Date(dueDate) < now;
}

function escapeHtml(str) {
    if (!str) return '';
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// Boards

function findBoard(id) {
    return data.boards.find(b => b.id === id);
}

function selectBoard(id) {
    board = findBoard(id) || data.boards[0];
    if (location.hash.slice(1) !== board.id) history.replaceState(null, '', `#${board.id}`);
    renderBoardSelect();
    renderBoard();
}

function renderBoardSelect() {
    const select = document.getElementById('boardSelect');
    select.innerHTML = data.boards
        .map(b => `<option value="${b.id}">${escapeHtml(b.name)}</option>`)
        .join('');
    select.value = board.id;
}

function addBoard(name) {
    const newBoard = { id: newId('board'), name, tasks: [] };
    data.boards.push(newBoard);
    selectBoard(newBoard.id);
    saveBoard();
    return newBoard;
}

function openBoardModal(isNew) {
    document.getElementById('modalBoardTitle').innerText = isNew ? 'New Board' : 'Edit Board';
    document.getElementById('boardIsNew').value = isNew ? '1' : '';
    document.getElementById('boardName').value = isNew ? '' : board.name;
    // The last board can't be deleted
    document.getElementById('btnDeleteBoard').classList.toggle('hidden', isNew || data.boards.length < 2);
    openModal('boardModal');
    document.getElementById('boardName').focus();
}

function saveBoardForm(e) {
    e.preventDefault();
    const name = document.getElementById('boardName').value.trim();
    if (document.getElementById('boardIsNew').value) {
        addBoard(name);
        notify(`Board created: ${name}`);
    } else {
        board.name = name;
        renderBoardSelect();
        saveBoard();
        notify(`Board renamed: ${name}`);
    }
    closeModal('boardModal');
}

function deleteCurrentBoard() {
    const count = board.tasks.length;
    if (count && !confirm(`Delete "${board.name}" and its ${count} ticket${count > 1 ? 's' : ''}?`)) return;
    const name = board.name;
    data.boards = data.boards.filter(b => b !== board);
    selectBoard(data.boards[0].id);
    saveBoard();
    closeModal('boardModal');
    notify(`Board deleted: ${name}`);
}

// Board rendering

function renderColumns() {
    document.getElementById('board').innerHTML = COLUMNS.map(col => `
        <div class="column">
            <div class="column-header">
                <h2><span class="dot" style="background: ${col.color}"></span>${col.name}</h2>
                <span id="count-${col.id}" class="count">0</span>
            </div>
            <div id="col-${col.id}" ondragover="handleDragOver(event)" ondragleave="handleDragLeave(event)" ondrop="handleDrop(event, '${col.id}')" class="column-body"></div>
        </div>
    `).join('');

    document.getElementById('taskColumn').innerHTML = COLUMNS
        .map(col => `<option value="${col.id}">${col.name}</option>`)
        .join('');
}

function renderBoard() {
    const search = document.getElementById('searchInput').value.toLowerCase().trim();
    const matches = task => !search
        || task.title.toLowerCase().includes(search)
        || task.desc.toLowerCase().includes(search);

    COLUMNS.forEach(col => {
        const tasks = board.tasks.filter(t => t.column === col.id && matches(t));
        const container = document.getElementById(`col-${col.id}`);
        container.innerHTML = '';
        tasks.forEach(task => container.appendChild(createCard(task)));
        document.getElementById(`count-${col.id}`).innerText = tasks.length;
    });
}

function createCard(task) {
    const card = document.createElement('div');
    card.className = 'card';
    card.draggable = true;

    card.addEventListener('dragstart', e => {
        draggedTaskId = task.id;
        card.classList.add('dragging');
        e.dataTransfer.setData('text/plain', task.id);
    });
    card.addEventListener('dragend', () => {
        card.classList.remove('dragging');
        draggedTaskId = null;
    });
    card.addEventListener('click', () => openTaskModal(task.id));

    const colIndex = COLUMNS.findIndex(c => c.id === task.column);
    const prevCol = COLUMNS[colIndex - 1];
    const nextCol = COLUMNS[colIndex + 1];
    const overdue = isOverdue(task.dueDate) && task.column !== 'done';
    const blockerTitles = task.blockedBy.map(id => findTask(id)?.title).filter(Boolean).join(', ');

    card.innerHTML = `
        ${isBlocked(task) ? `
            <span class="badge-blocked" title="Blocked by: ${escapeHtml(blockerTitles)}">
                Blocked
            </span>` : ''}

        <h3 class="card-title">${escapeHtml(task.title)}</h3>
        ${task.desc ? `<p class="card-desc">${escapeHtml(task.desc)}</p>` : ''}

        <div class="card-footer">
            <div class="card-meta">
                ${task.dueDate ? `<span class="${overdue ? 'overdue' : ''}">Due ${task.dueDate.slice(5)}</span>` : ''}
                ${task.comments.length ? `<span>${task.comments.length} comment${task.comments.length > 1 ? 's' : ''}</span>` : ''}
            </div>
            <div class="card-moves">
                ${prevCol ? `<button onclick="event.stopPropagation(); moveTask('${task.id}', '${prevCol.id}')" class="btn btn-secondary btn-sm" title="Move to ${prevCol.name}">&larr;</button>` : ''}
                ${nextCol ? `<button onclick="event.stopPropagation(); moveTask('${task.id}', '${nextCol.id}')" class="btn btn-secondary btn-sm" title="Move to ${nextCol.name}">&rarr;</button>` : ''}
            </div>
        </div>
    `;

    return card;
}

// Drag & drop

function handleDragOver(e) {
    e.preventDefault();
    e.currentTarget.classList.add('drag-over');
}

function handleDragLeave(e) {
    e.currentTarget.classList.remove('drag-over');
}

function handleDrop(e, column) {
    e.preventDefault();
    e.currentTarget.classList.remove('drag-over');
    const taskId = e.dataTransfer.getData('text/plain') || draggedTaskId;
    if (taskId) moveTask(taskId, column);
}

// Task actions

function moveTask(taskId, column) {
    const task = findTask(taskId);
    if (!task) return false;
    task.column = column;
    saveBoard();
    return true;
}

function addTask(fields) {
    const task = normalizeTask({ id: newId('task'), column: 'backlog', ...fields });
    board.tasks.push(task);
    saveBoard();
    return task;
}

function addComment(taskId, text) {
    const task = findTask(taskId);
    if (!task || !text) return null;
    const comment = { id: newId('comment'), text, createdAt: new Date().toISOString() };
    task.comments.push(comment);
    saveBoard();
    return comment;
}

// Modals

function openModal(id) {
    document.getElementById(id).classList.remove('hidden');
}

function closeModal(id) {
    document.getElementById(id).classList.add('hidden');
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

    modalBlockers = task ? [...task.blockedBy] : [];
    renderBlockers();
    openModal('taskModal');
}

function saveTaskForm(e) {
    e.preventDefault();
    const fields = {
        title: document.getElementById('taskTitle').value.trim(),
        desc: document.getElementById('taskDesc').value.trim(),
        column: document.getElementById('taskColumn').value,
        dueDate: document.getElementById('taskDueDate').value,
        blockedBy: modalBlockers
    };

    const task = findTask(document.getElementById('taskId').value);
    if (task) {
        Object.assign(task, fields);
        saveBoard();
        notify(`Ticket updated: ${task.title}`);
    } else {
        addTask(fields);
        notify(`Ticket created: ${fields.title}`);
    }
    closeModal('taskModal');
}

function deleteCurrentTask() {
    const taskId = document.getElementById('taskId').value;
    const title = findTask(taskId).title;
    board.tasks = board.tasks.filter(t => t.id !== taskId);
    board.tasks.forEach(t => {
        t.blockedBy = t.blockedBy.filter(id => id !== taskId);
    });
    saveBoard();
    closeModal('taskModal');
    notify(`Ticket deleted: ${title}`);
}

// Blockers (kept in modalBlockers until the form is saved)

function renderBlockers() {
    const currentId = document.getElementById('taskId').value;

    document.getElementById('blockerList').innerHTML = modalBlockers.map(id => {
        const blocker = findTask(id);
        if (!blocker) return '';
        return `
            <li class="list-item">
                <span class="${blocker.column === 'done' ? 'done-text' : ''}">${escapeHtml(blocker.title)}</span>
                <button type="button" onclick="removeBlocker('${id}')" class="icon-btn">
                    &times;
                </button>
            </li>
        `;
    }).join('');

    const options = board.tasks.filter(t => t.id !== currentId && !modalBlockers.includes(t.id));
    document.getElementById('blockerSelect').innerHTML =
        '<option value="">+ Add blocking ticket...</option>' +
        options.map(t => `<option value="${t.id}">${escapeHtml(t.title)}</option>`).join('');
}

function addBlocker(id) {
    if (!id) return;
    modalBlockers.push(id);
    renderBlockers();
}

function removeBlocker(id) {
    modalBlockers = modalBlockers.filter(b => b !== id);
    renderBlockers();
}

// Comments

function renderComments(task) {
    const list = document.getElementById('commentList');
    if (!task.comments.length) {
        list.innerHTML = '<li class="empty">No comments yet.</li>';
        return;
    }
    list.innerHTML = task.comments.map(c => `
        <li class="list-item stacked">
            <div class="item-date">${new Date(c.createdAt).toLocaleString()}</div>
            <p class="item-text">${escapeHtml(c.text)}</p>
        </li>
    `).join('');
}

function addCommentFromInput() {
    const taskId = document.getElementById('taskId').value;
    const input = document.getElementById('commentInput');
    if (!addComment(taskId, input.value.trim())) return;
    input.value = '';
    renderComments(findTask(taskId));
}

// Export / external API for the local LLM

function getBoardState() {
    return {
        board: { id: board.id, name: board.name },
        columns: COLUMNS.map(c => c.id),
        tasks: board.tasks.map(t => ({ ...t, blocked: isBlocked(t) }))
    };
}

function copyBoardJson() {
    const json = JSON.stringify(getBoardState(), null, 2);

    // navigator.clipboard is only available on https / localhost
    if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(json);
    } else {
        const textArea = document.createElement('textarea');
        textArea.value = json;
        document.body.appendChild(textArea);
        textArea.select();
        document.execCommand('copy');
        document.body.removeChild(textArea);
    }
    notify('Board copied as JSON');
}

function listBoards() {
    return data.boards.map(b => ({ id: b.id, name: b.name, tasks: b.tasks.length }));
}

window.ScrumAI = {
    listBoards,
    selectBoard,
    addBoard,
    getBoardState,
    addTask,
    moveTask,
    addComment
};

// Theme

function applyTheme(theme) {
    document.documentElement.dataset.theme = theme;
    document.getElementById('themeToggle').innerText = theme === 'dark' ? 'Light' : 'Dark';
}

function toggleTheme() {
    const theme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    localStorage.setItem(THEME_KEY, theme);
    applyTheme(theme);
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
    localStorage.setItem(NOTIFICATIONS_KEY, JSON.stringify(notifications));
    renderNotifications();
}

function renderNotifications() {
    document.getElementById('notificationCount').innerText = notifications.length;
    const list = document.getElementById('notificationList');
    if (!notifications.length) {
        list.innerHTML = '<li class="empty">No notifications.</li>';
        return;
    }
    list.innerHTML = notifications.map(n => `
        <li class="list-item stacked">
            <div class="item-date">${new Date(n.createdAt).toLocaleString()}</div>
            <p class="item-text">${escapeHtml(n.message)}</p>
        </li>
    `).join('');
}

function toggleNotifications() {
    document.getElementById('notificationPanel').classList.toggle('hidden');
}

function closeNotifications() {
    document.getElementById('notificationPanel').classList.add('hidden');
}
