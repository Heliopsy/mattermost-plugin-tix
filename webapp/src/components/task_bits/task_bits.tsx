// SPDX-License-Identifier: AGPL-3.0-only

import {FormattedMessage} from 'react-intl';

import {Tag} from '@mattermost/compass-ui/components/tag';
import type {TagType} from '@mattermost/compass-ui/components/tag';

import type {Person, StateCategory, TaskState} from 'types/tix';

const tagTypes: Record<string, TagType> = {
    todo: 'default',
    in_progress: 'info',
    blocked: 'danger',
    waiting: 'warning',
    done: 'success',
    cancelled: 'default',
};

// StatusTag shows a workflow state as a pill coloured by its category, never by tenant colour.
export function StatusTag({state}: {state: TaskState}) {
    return (
        <Tag
            label={state.label || state.key}
            type={tagTypes[state.category] ?? 'default'}
            size='x-small'
        />
    );
}

// Categories in the order the sidebar groups them.
export const CATEGORY_ORDER: StateCategory[] = ['in_progress', 'todo', 'blocked', 'waiting', 'done', 'cancelled', ''];

const categoryMessages = {
    todo: {id: 'tix.category.todo', defaultMessage: 'To do'},
    in_progress: {id: 'tix.category.in_progress', defaultMessage: 'In progress'},
    blocked: {id: 'tix.category.blocked', defaultMessage: 'Blocked'},
    waiting: {id: 'tix.category.waiting', defaultMessage: 'Waiting'},
    done: {id: 'tix.category.done', defaultMessage: 'Done'},
    cancelled: {id: 'tix.category.cancelled', defaultMessage: 'Cancelled'},
    other: {id: 'tix.category.other', defaultMessage: 'Other'},
} as const;

export function CategoryName({category}: {category: StateCategory}) {
    return <FormattedMessage {...(categoryMessages[category as keyof typeof categoryMessages] ?? categoryMessages.other)}/>;
}

// personLabel names a person by their Mattermost @username when linked, else by their tix name.
export function personLabel(p: Person | null | undefined) {
    if (!p) {
        return '';
    }
    return p.username ? `@${p.username}` : p.name;
}

export const priorityMessages = {
    1: {id: 'tix.priority.1', defaultMessage: 'Highest'},
    2: {id: 'tix.priority.2', defaultMessage: 'High'},
    3: {id: 'tix.priority.3', defaultMessage: 'Normal'},
    4: {id: 'tix.priority.4', defaultMessage: 'Low'},
    5: {id: 'tix.priority.5', defaultMessage: 'Lowest'},
} as const;

export function PriorityName({priority}: {priority: number}) {
    const m = priorityMessages[priority as keyof typeof priorityMessages];
    if (!m) {
        return <>{priority}</>;
    }
    return <FormattedMessage {...m}/>;
}

// safeHref keeps only http and https links from tix data.
export function safeHref(url: string | undefined) {
    if (!url) {
        return undefined;
    }
    try {
        const u = new URL(url, window.location.href);
        return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : undefined;
    } catch {
        return undefined;
    }
}
