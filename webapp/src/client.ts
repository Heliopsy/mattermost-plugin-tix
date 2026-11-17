// SPDX-License-Identifier: AGPL-3.0-only

import manifest from 'manifest';

import type {Options} from '@mattermost/types/client4';

import {Client4} from 'mattermost-redux/client';

import type {Comment, CreateTaskInput, Me, Project, Task, TaskDetail, TaskFilter, TaskList, UpdateTaskInput} from 'types/tix';

let basePath = '';

// Keep only the path of the SiteURL so requests stay same-origin even when the
// browser reached the server through a different hostname.
export function setSiteURL(siteURL: string | undefined) {
    basePath = siteURL ? new URL(siteURL).pathname.replace(/\/$/, '') : '';
    Client4.setUrl(basePath);
}

export function pluginURL(path: string) {
    return `${basePath}/plugins/${manifest.id}${path}`;
}

// ApiError carries the server's error code, such as not_linked, so the UI can react to it.
export class ApiError extends Error {
    status: number;
    code: string;

    constructor(message: string, status: number, code: string) {
        super(message);
        this.status = status;
        this.code = code;
    }
}

export function isNotLinked(err: unknown) {
    return err instanceof ApiError && (err.code === 'not_linked' || err.code === 'token_rejected');
}

export async function doFetch<T>(path: string, options: Options = {}): Promise<T> {
    // getOptions adds X-Requested-With and, for non-GET requests, the session's CSRF
    // token. Without the token the server forwards the request as unauthenticated.
    const response = await fetch(
        pluginURL(`/api/v1${path}`),
        Client4.getOptions(options),
    );

    const body = await response.json().catch(() => null);
    if (!response.ok) {
        throw new ApiError(body?.error ?? `Request failed with status ${response.status}`, response.status, body?.code ?? '');
    }
    return body as T;
}

const send = <T>(method: string, path: string, body?: unknown) => doFetch<T>(path, {method, body: body === undefined ? undefined : JSON.stringify(body)});
const taskPath = (ref: string) => `/tasks/${encodeURIComponent(ref)}`;

export const getMe = () => doFetch<Me>('/me');
export const connect = () => send<{message: string}>('POST', '/connect');
export const getProjects = () => doFetch<Project[]>('/projects');

export function listTasks(filter: TaskFilter) {
    const q = new URLSearchParams();
    if (filter.mine) {
        q.set('mine', 'true');
    }
    if (filter.open) {
        q.set('open', 'true');
    }
    if (filter.project) {
        q.set('project', filter.project);
    }
    if (filter.q) {
        q.set('q', filter.q);
    }
    filter.status?.forEach((s) => q.append('status', s));
    if (filter.cursor) {
        q.set('cursor', filter.cursor);
    }
    if (filter.limit) {
        q.set('limit', String(filter.limit));
    }
    return doFetch<TaskList>(`/tasks?${q.toString()}`);
}

export const getTask = (ref: string) => doFetch<TaskDetail>(taskPath(ref));
export const createTask = (input: CreateTaskInput) => send<Task>('POST', '/tasks', input);
export const updateTask = (ref: string, input: UpdateTaskInput) => send<Task>('PATCH', taskPath(ref), input);
export const transitionTask = (ref: string, to: string, comment?: string) => send<Task>('POST', `${taskPath(ref)}/transition`, {to, comment});
export const addComment = (ref: string, body: string) => send<Comment>('POST', `${taskPath(ref)}/comments`, {body});

// postMessage posts as the signed-in user through the Mattermost REST API, the way the host's own composer does.
export function postMessage(channelId: string, rootId: string, message: string) {
    return Client4.createPost({channel_id: channelId, root_id: rootId, message});
}
