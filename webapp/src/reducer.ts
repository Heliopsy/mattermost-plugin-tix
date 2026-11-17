// SPDX-License-Identifier: AGPL-3.0-only

import manifest from 'manifest';
import type {UnknownAction} from 'redux';

import type {GlobalState} from '@mattermost/types/store';

// CreatePrefill is what the create modal starts from: a post it was opened on, or nothing.
export type CreatePrefill = {
    channelId: string;
    title?: string;
    body?: string;

    // postId and rootId are set when the task is made from a post, so a link can go into its thread.
    postId?: string;
    rootId?: string;
};

export type PluginState = {
    create: CreatePrefill | null;

    // openRef asks the sidebar to show a task; refresh bumps after a change made elsewhere.
    openRef: string | null;
    refresh: number;
};

const OPEN_CREATE = `${manifest.id}/open_create`;
const CLOSE_CREATE = `${manifest.id}/close_create`;
const SHOW_TASK = `${manifest.id}/show_task`;
const TASKS_CHANGED = `${manifest.id}/tasks_changed`;

export const openCreate = (prefill: CreatePrefill) => ({type: OPEN_CREATE, prefill});
export const closeCreate = () => ({type: CLOSE_CREATE});
export const showTask = (ref: string | null) => ({type: SHOW_TASK, ref});
export const tasksChanged = () => ({type: TASKS_CHANGED});

export const initialState: PluginState = {create: null, openRef: null, refresh: 0};

export default function reducer(state: PluginState = initialState, action: UnknownAction): PluginState {
    switch (action.type) {
    case OPEN_CREATE:
        return {...state, create: action.prefill as CreatePrefill};
    case CLOSE_CREATE:
        return {...state, create: null};
    case SHOW_TASK:
        return {...state, openRef: action.ref as string | null};
    case TASKS_CHANGED:
        return {...state, refresh: state.refresh + 1};
    default:
        return state;
    }
}

export const pluginState = (state: GlobalState): PluginState =>
    (state as unknown as Record<string, PluginState | undefined>)[`plugins-${manifest.id}`] ?? initialState;
