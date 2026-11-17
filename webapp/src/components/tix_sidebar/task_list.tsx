// SPDX-License-Identifier: AGPL-3.0-only

import {FormattedMessage, useIntl} from 'react-intl';

import {Button} from '@mattermost/compass-ui/components/button';
import {EmptyState} from '@mattermost/compass-ui/components/empty-state';
import {ErrorMessage} from '@mattermost/compass-ui/components/error-message';
import {Spinner} from '@mattermost/compass-ui/components/spinner';

import {CATEGORY_ORDER, CategoryName, PriorityName, StatusTag, personLabel} from 'components/task_bits/task_bits';

import type {StateCategory, Task} from 'types/tix';

import type {TasksState} from './use_tasks';

type Props = {
    tasks: TasksState;
    grouped?: boolean;
    empty: React.ReactNode;
    onOpen: (ref: string) => void;
};

function TaskRow({task, onOpen}: {task: Task; onOpen: (ref: string) => void}) {
    return (
        <li className='TixPlugin-TaskList__item'>
            <button
                type='button'
                className='TixPlugin-TaskList__row'
                onClick={() => onOpen(task.ref)}
            >
                <span className='TixPlugin-TaskList__top'>
                    <span className='TixPlugin-TaskList__ref'>{task.ref}</span>
                    <StatusTag state={task.state}/>
                </span>
                <span className='TixPlugin-TaskList__title'>{task.title}</span>
                <span className='TixPlugin-TaskList__meta'>
                    <PriorityName priority={task.priority}/>
                    {task.assignee && (
                        <span>{personLabel(task.assignee)}</span>
                    )}
                </span>
            </button>
        </li>
    );
}

function groups(items: Task[]) {
    const by = new Map<StateCategory, Task[]>();
    for (const t of items) {
        const c = CATEGORY_ORDER.includes(t.state.category) ? t.state.category : '';
        by.set(c, [...(by.get(c) ?? []), t]);
    }
    return CATEGORY_ORDER.filter((c) => by.has(c)).map((c) => ({category: c, tasks: by.get(c)!}));
}

// TaskList shows a page of tasks with loading, empty and error states, optionally grouped by status category.
export default function TaskList({tasks, grouped, empty, onOpen}: Props) {
    const intl = useIntl();
    if (tasks.error) {
        return (
            <div className='TixPlugin-TaskList__state'>
                <ErrorMessage message={tasks.error}/>
                <Button
                    emphasis='tertiary'
                    size='small'
                    onClick={tasks.reload}
                >
                    <FormattedMessage
                        id='tix.retry'
                        defaultMessage='Try again'
                    />
                </Button>
            </div>
        );
    }
    if (tasks.loading && tasks.items.length === 0) {
        return (
            <div className='TixPlugin-TaskList__state'>
                <Spinner
                    size='24'
                    aria-label={intl.formatMessage({id: 'tix.loadingTasks', defaultMessage: 'Loading tasks'})}
                />
            </div>
        );
    }
    if (tasks.items.length === 0) {
        return (
            <EmptyState
                className='TixPlugin-TaskList__state'
                title={empty}
            />
        );
    }

    const sections = grouped ? groups(tasks.items) : [{category: null, tasks: tasks.items}];
    return (
        <div
            className='TixPlugin-TaskList'
            aria-busy={tasks.loading}
        >
            {sections.map((s) => (
                <section
                    key={s.category ?? 'all'}
                    className='TixPlugin-TaskList__group'
                >
                    {s.category !== null && (
                        <h3 className='TixPlugin-TaskList__heading'>
                            <CategoryName category={s.category}/>
                            <span className='TixPlugin-TaskList__count'>{s.tasks.length}</span>
                        </h3>
                    )}
                    <ul className='TixPlugin-TaskList__items'>
                        {s.tasks.map((t) => (
                            <TaskRow
                                key={t.id}
                                task={t}
                                onOpen={onOpen}
                            />
                        ))}
                    </ul>
                </section>
            ))}
            {tasks.nextCursor && (
                <div className='TixPlugin-TaskList__more'>
                    <Button
                        emphasis='tertiary'
                        size='small'
                        loading={tasks.loading}
                        onClick={tasks.loadMore}
                    >
                        <FormattedMessage
                            id='tix.loadMore'
                            defaultMessage='Load more'
                        />
                    </Button>
                </div>
            )}
        </div>
    );
}
