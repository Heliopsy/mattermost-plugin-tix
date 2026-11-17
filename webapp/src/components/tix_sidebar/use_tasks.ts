// SPDX-License-Identifier: AGPL-3.0-only

import {isNotLinked, listTasks} from 'client';
import {useCallback, useEffect, useRef, useState} from 'react';

import type {Task, TaskFilter} from 'types/tix';

export type TasksState = {
    loading: boolean;
    error: string;
    items: Task[];
    nextCursor: string;
    loadMore: () => void;
    reload: () => void;
};

// useTasks lists tasks for a filter and reloads when the filter or refresh changes.
// skip leaves the list empty, e.g. until a project is picked.
export default function useTasks(filter: TaskFilter, refresh: number, onNotLinked: () => void, skip = false): TasksState {
    const [loading, setLoading] = useState(!skip);
    const [error, setError] = useState('');
    const [items, setItems] = useState<Task[]>([]);
    const [nextCursor, setNextCursor] = useState('');
    const [reloads, setReloads] = useState(0);
    const key = JSON.stringify(filter);
    const seq = useRef(0);

    const load = useCallback(async (cursor: string) => {
        const mine = ++seq.current;
        setLoading(true);
        setError('');
        try {
            const page = await listTasks({...JSON.parse(key), cursor});
            if (mine !== seq.current) {
                return;
            }
            setItems((prev) => (cursor ? [...prev, ...page.items] : page.items));
            setNextCursor(page.next_cursor);
        } catch (e) {
            if (mine !== seq.current) {
                return;
            }
            if (isNotLinked(e)) {
                onNotLinked();
                return;
            }
            setError((e as Error).message);
        } finally {
            if (mine === seq.current) {
                setLoading(false);
            }
        }
    }, [key, onNotLinked]);

    useEffect(() => {
        if (skip) {
            seq.current++;
            setItems([]);
            setNextCursor('');
            setLoading(false);
            return;
        }
        load('');
    }, [load, refresh, reloads, skip]);

    return {
        loading,
        error,
        items,
        nextCursor,
        loadMore: () => load(nextCursor),
        reload: () => setReloads((n) => n + 1),
    };
}
