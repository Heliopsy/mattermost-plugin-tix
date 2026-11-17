// SPDX-License-Identifier: AGPL-3.0-only

import {addComment, getTask, isNotLinked, transitionTask, updateTask} from 'client';
import {useCallback, useEffect, useState} from 'react';
import {FormattedMessage, useIntl} from 'react-intl';
import {useSelector} from 'react-redux';

import ArrowLeftIcon from '@mattermost/compass-icons/components/arrow-left';
import OpenInNewIcon from '@mattermost/compass-icons/components/open-in-new';
import {Button} from '@mattermost/compass-ui/components/button';
import {ErrorMessage} from '@mattermost/compass-ui/components/error-message';
import {Icon} from '@mattermost/compass-ui/components/icon';
import {IconButton} from '@mattermost/compass-ui/components/icon-button';
import {Select} from '@mattermost/compass-ui/components/select';
import {Spinner} from '@mattermost/compass-ui/components/spinner';
import {Tag} from '@mattermost/compass-ui/components/tag';
import {TextArea} from '@mattermost/compass-ui/components/text-area';

import {getCurrentUserId} from 'mattermost-redux/selectors/entities/common';

import ExternalLink from 'components/external_link/external_link';
import Markdown from 'components/markdown/markdown';
import {StatusTag, personLabel, priorityMessages, safeHref} from 'components/task_bits/task_bits';

import type {Move, TaskDetail as Detail, UpdateTaskInput} from 'types/tix';
import {PRIORITIES} from 'types/tix';

type Props = {
    taskRef: string;
    onBack: () => void;
    onChanged: () => void;
    onNotLinked: () => void;
};

// TaskDetail shows one task: its state, people, description and comments, and lets the user move,
// reassign, reprioritise and comment on it.
export default function TaskDetail({taskRef, onBack, onChanged, onNotLinked}: Props) {
    const intl = useIntl();
    const currentUserId = useSelector(getCurrentUserId);
    const [detail, setDetail] = useState<Detail | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);
    const [actionError, setActionError] = useState('');
    const [pendingMove, setPendingMove] = useState<Move | null>(null);
    const [moveComment, setMoveComment] = useState('');
    const [comment, setComment] = useState('');

    const load = useCallback(async () => {
        setLoading(true);
        setError('');
        try {
            setDetail(await getTask(taskRef));
        } catch (e) {
            if (isNotLinked(e)) {
                onNotLinked();
                return;
            }
            setError((e as Error).message);
        } finally {
            setLoading(false);
        }
    }, [taskRef, onNotLinked]);

    useEffect(() => {
        load();
    }, [load]);

    // act runs one change, then reloads the task and tells the lists.
    const act = async (fn: () => Promise<unknown>) => {
        setBusy(true);
        setActionError('');
        try {
            await fn();
            onChanged();
            await load();
            return true;
        } catch (e) {
            if (isNotLinked(e)) {
                onNotLinked();
                return false;
            }
            setActionError((e as Error).message);
            return false;
        } finally {
            setBusy(false);
        }
    };

    const move = async (m: Move, text?: string) => {
        if (await act(() => transitionTask(taskRef, m.to, text))) {
            setPendingMove(null);
            setMoveComment('');
        }
    };

    const update = (input: UpdateTaskInput) => act(() => updateTask(taskRef, input));

    const submitComment = async () => {
        if (await act(() => addComment(taskRef, comment))) {
            setComment('');
        }
    };

    const back = (
        <IconButton
            icon={<Icon glyph={<ArrowLeftIcon/>}/>}
            size='small'
            aria-label={intl.formatMessage({id: 'tix.detail.back', defaultMessage: 'Back to tasks'})}
            onClick={onBack}
        />
    );

    if (loading && !detail) {
        return (
            <div className='TixPlugin-TaskDetail'>
                <div className='TixPlugin-TaskDetail__nav'>{back}</div>
                <div className='TixPlugin-TaskList__state'>
                    <Spinner
                        size='24'
                        aria-label={intl.formatMessage({id: 'tix.detail.loading', defaultMessage: 'Loading the task'})}
                    />
                </div>
            </div>
        );
    }
    if (error || !detail) {
        return (
            <div className='TixPlugin-TaskDetail'>
                <div className='TixPlugin-TaskDetail__nav'>{back}</div>
                <div className='TixPlugin-TaskList__state'>
                    <ErrorMessage message={error}/>
                    <Button
                        emphasis='tertiary'
                        size='small'
                        onClick={load}
                    >
                        <FormattedMessage
                            id='tix.retry'
                            defaultMessage='Try again'
                        />
                    </Button>
                </div>
            </div>
        );
    }

    const {task, comments, transitions} = detail;
    const href = safeHref(task.web_url);
    const assignedToMe = task.assignee?.user_id === currentUserId;
    const priorityOptions = PRIORITIES.map((p) => ({value: String(p), label: intl.formatMessage(priorityMessages[p])}));

    return (
        <div
            className='TixPlugin-TaskDetail'
            aria-busy={busy}
        >
            <div className='TixPlugin-TaskDetail__nav'>
                {back}
                <span className='TixPlugin-TaskDetail__ref'>{task.ref}</span>
                {href && (
                    <ExternalLink
                        className='TixPlugin-TaskDetail__open'
                        href={href}
                    >
                        <FormattedMessage
                            id='tix.detail.openInTix'
                            defaultMessage='Open in tix'
                        />
                        <Icon
                            size='16'
                            glyph={<OpenInNewIcon/>}
                        />
                    </ExternalLink>
                )}
            </div>

            <h2 className='TixPlugin-TaskDetail__title'>{task.title}</h2>
            <div className='TixPlugin-TaskDetail__status'>
                <StatusTag state={task.state}/>
                {task.blocked && (
                    <Tag
                        type='danger'
                        size='x-small'
                        label={intl.formatMessage({id: 'tix.detail.waiting', defaultMessage: 'Waiting on another task'})}
                    />
                )}
            </div>

            {actionError && (
                <div aria-live='assertive'>
                    <ErrorMessage
                        className='TixPlugin-TaskDetail__error'
                        message={actionError}
                    />
                </div>
            )}

            {transitions.length > 0 && (
                <section className='TixPlugin-TaskDetail__section'>
                    <h3 className='TixPlugin-TaskDetail__label'>
                        <FormattedMessage
                            id='tix.detail.moveTo'
                            defaultMessage='Move to'
                        />
                    </h3>
                    <div className='TixPlugin-TaskDetail__moves'>
                        {transitions.map((m) => (
                            <Button
                                key={m.to}
                                emphasis={pendingMove?.to === m.to ? 'secondary' : 'tertiary'}
                                size='small'
                                disabled={busy}
                                onClick={() => (m.requires_comment ? setPendingMove(m) : move(m))}
                            >
                                {m.label || m.to}
                            </Button>
                        ))}
                    </div>
                    {pendingMove && (
                        <div className='TixPlugin-TaskDetail__moveComment'>
                            <TextArea
                                label={intl.formatMessage(
                                    {id: 'tix.detail.moveComment', defaultMessage: 'Moving to {state} needs a comment'},
                                    {state: pendingMove.label || pendingMove.to},
                                )}
                                value={moveComment}
                                onChange={(e) => setMoveComment(e.target.value)}
                            />
                            <div className='TixPlugin-TaskDetail__actions'>
                                <Button
                                    size='small'
                                    disabled={busy || !moveComment.trim()}
                                    onClick={() => move(pendingMove, moveComment)}
                                >
                                    <FormattedMessage
                                        id='tix.detail.moveConfirm'
                                        defaultMessage='Move to {state}'
                                        values={{state: pendingMove.label || pendingMove.to}}
                                    />
                                </Button>
                                <Button
                                    size='small'
                                    emphasis='tertiary'
                                    onClick={() => setPendingMove(null)}
                                >
                                    <FormattedMessage
                                        id='tix.cancel'
                                        defaultMessage='Cancel'
                                    />
                                </Button>
                            </div>
                        </div>
                    )}
                </section>
            )}

            <dl className='TixPlugin-TaskDetail__fields'>
                <dt className='TixPlugin-TaskDetail__term'>
                    <FormattedMessage
                        id='tix.field.assignee'
                        defaultMessage='Assignee'
                    />
                </dt>
                <dd className='TixPlugin-TaskDetail__value'>
                    <span>
                        {task.assignee ? personLabel(task.assignee) : (
                            <FormattedMessage
                                id='tix.field.unassigned'
                                defaultMessage='Nobody'
                            />
                        )}
                    </span>
                    {!assignedToMe && (
                        <Button
                            size='x-small'
                            emphasis='tertiary'
                            disabled={busy}
                            onClick={() => update({assignee_user_id: currentUserId})}
                        >
                            <FormattedMessage
                                id='tix.field.assignMe'
                                defaultMessage='Assign to me'
                            />
                        </Button>
                    )}
                    {task.assignee && (
                        <Button
                            size='x-small'
                            emphasis='tertiary'
                            disabled={busy}
                            onClick={() => update({assignee_user_id: ''})}
                        >
                            <FormattedMessage
                                id='tix.field.unassign'
                                defaultMessage='Unassign'
                            />
                        </Button>
                    )}
                </dd>
                <dt className='TixPlugin-TaskDetail__term'>
                    <FormattedMessage
                        id='tix.field.priority'
                        defaultMessage='Priority'
                    />
                </dt>
                <dd className='TixPlugin-TaskDetail__value'>
                    <Select
                        size='small'
                        aria-label={intl.formatMessage({id: 'tix.field.priority', defaultMessage: 'Priority'})}
                        options={priorityOptions}
                        value={String(task.priority)}
                        disabled={busy}
                        onChange={(v) => update({priority: Number(v)})}
                    />
                </dd>
                <dt className='TixPlugin-TaskDetail__term'>
                    <FormattedMessage
                        id='tix.field.project'
                        defaultMessage='Project'
                    />
                </dt>
                <dd className='TixPlugin-TaskDetail__value'>{task.project_name || task.project_key}</dd>
                {task.tags.length > 0 && (
                    <>
                        <dt className='TixPlugin-TaskDetail__term'>
                            <FormattedMessage
                                id='tix.field.tags'
                                defaultMessage='Tags'
                            />
                        </dt>
                        <dd className='TixPlugin-TaskDetail__value TixPlugin-TaskDetail__tags'>
                            {task.tags.map((t) => (
                                <Tag
                                    key={t}
                                    label={t}
                                    size='x-small'
                                    casing='title-case'
                                />
                            ))}
                        </dd>
                    </>
                )}
            </dl>

            {task.body && (
                <section className='TixPlugin-TaskDetail__section'>
                    <h3 className='TixPlugin-TaskDetail__label'>
                        <FormattedMessage
                            id='tix.field.description'
                            defaultMessage='Description'
                        />
                    </h3>
                    <Markdown text={task.body}/>
                </section>
            )}

            <section className='TixPlugin-TaskDetail__section'>
                <h3 className='TixPlugin-TaskDetail__label'>
                    <FormattedMessage
                        id='tix.detail.comments'
                        defaultMessage='Comments ({count})'
                        values={{count: comments.length}}
                    />
                </h3>
                <ol
                    className='TixPlugin-TaskDetail__comments'
                    aria-live='polite'
                >
                    {comments.map((c) => (
                        <li
                            key={c.id}
                            className='TixPlugin-TaskDetail__comment'
                        >
                            <div className='TixPlugin-TaskDetail__commentHead'>
                                <strong>{personLabel(c.author)}</strong>
                                <time dateTime={c.created_at}>{intl.formatDate(c.created_at, {dateStyle: 'medium', timeStyle: 'short'})}</time>
                            </div>
                            <Markdown text={c.body}/>
                        </li>
                    ))}
                </ol>
                <TextArea
                    label={intl.formatMessage({id: 'tix.detail.addComment', defaultMessage: 'Add a comment'})}
                    value={comment}
                    onChange={(e) => setComment(e.target.value)}
                />
                <div className='TixPlugin-TaskDetail__actions'>
                    <Button
                        size='small'
                        disabled={busy || !comment.trim()}
                        onClick={submitComment}
                    >
                        <FormattedMessage
                            id='tix.detail.comment'
                            defaultMessage='Comment'
                        />
                    </Button>
                </div>
            </section>
        </div>
    );
}
