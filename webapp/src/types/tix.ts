// SPDX-License-Identifier: AGPL-3.0-only

// Shapes of the plugin's /api/v1 answers (server/webapi).

export type StateCategory = 'todo' | 'in_progress' | 'blocked' | 'waiting' | 'done' | 'cancelled' | '';

export type Person = {
    actor_id: string;
    name: string;
    user_id?: string;
    username?: string;
};

export type TaskState = {
    key: string;
    label: string;
    category: StateCategory;
    terminal: boolean;
};

export type Task = {
    id: string;
    ref: string;
    title: string;
    body: string;
    status: string;
    priority: number;
    project_id: string;
    project_key: string;
    project_name: string;
    tags: string[];
    blocked: boolean;
    due_at?: string;
    created_at: string;
    updated_at: string;
    version: number;
    web_url: string;
    state: TaskState;
    assignee: Person | null;
};

export type Move = {
    to: string;
    label: string;
    category: StateCategory;
    requires_comment: boolean;
};

export type Comment = {
    id: string;
    body: string;
    created_at: string;
    author: Person | null;
};

export type TaskDetail = {
    task: Task;
    comments: Comment[];
    transitions: Move[];
};

export type TaskList = {
    items: Task[];
    next_cursor: string;
};

export type Project = {
    id: string;
    key: string;
    name: string;
    color: string;
    workflow_id: string;
};

export type Me = {
    linked: boolean;
    tix_url: string;
    can_connect: boolean;
    actor?: Person;
    tenant_key?: string;

    // default_project is the default project of the channel asked about, if it has one.
    default_project?: string;
};

export type TaskFilter = {
    mine?: boolean;
    open?: boolean;
    project?: string;
    q?: string;
    status?: string[];
    cursor?: string;
    limit?: number;
};

export type CreateTaskInput = {
    project?: string;
    title: string;
    body?: string;
    priority?: number;
    assignee_user_id?: string;
};

export type UpdateTaskInput = {
    title?: string;
    priority?: number;
    assignee_user_id?: string;
};

// Priorities run from 1, the highest, to 5, the lowest.
export const PRIORITIES = [1, 2, 3, 4, 5] as const;
