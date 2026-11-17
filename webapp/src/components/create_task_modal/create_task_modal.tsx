// SPDX-License-Identifier: AGPL-3.0-only

import {createTask, getMe, getProjects, isNotLinked, postMessage} from 'client';
import {errorText} from 'i18n';
import {useEffect, useMemo, useRef, useState} from 'react';
import {FormattedMessage, useIntl} from 'react-intl';
import {useDispatch, useSelector} from 'react-redux';
import {closeCreate, pluginState, showTask, tasksChanged} from 'reducer';
import type {CreatePrefill} from 'reducer';

import {Button} from '@mattermost/compass-ui/components/button';
import {ErrorMessage} from '@mattermost/compass-ui/components/error-message';
import {Modal} from '@mattermost/compass-ui/components/modal';
import {Select} from '@mattermost/compass-ui/components/select';
import {Spinner} from '@mattermost/compass-ui/components/spinner';
import {TextArea} from '@mattermost/compass-ui/components/text-area';
import {TextInput} from '@mattermost/compass-ui/components/text-input';
import type {GlobalState} from '@mattermost/types/store';
import type {UserProfile} from '@mattermost/types/users';

import {getCurrentUserId} from 'mattermost-redux/selectors/entities/common';

import ConnectPrompt from 'components/connect_prompt/connect_prompt';
import ExternalLink from 'components/external_link/external_link';
import {priorityMessages, safeHref} from 'components/task_bits/task_bits';

import type {Me, Project, Task} from 'types/tix';
import {PRIORITIES} from 'types/tix';

import './create_task_modal.css';

const PROJECT_KEY = 'tix.create.project';
const UNASSIGNED = 'none';
const MAX_MEMBERS = 50;

function remembered() {
    try {
        return window.localStorage.getItem(PROJECT_KEY) ?? '';
    } catch {
        return '';
    }
}

function remember(value: string) {
    try {
        window.localStorage.setItem(PROJECT_KEY, value);
    } catch {
        // Storage can be off; the choice just isn't kept.
    }
}

const displayName = (u: UserProfile) => {
    const full = [u.first_name, u.last_name].filter(Boolean).join(' ');
    return full ? `@${u.username} (${full})` : `@${u.username}`;
};

// The loaded members of a channel the host knows about, for the assignee picker.
const memberIds = (state: GlobalState, channelId: string) => state.entities.users.profilesInChannel?.[channelId];
const profiles = (state: GlobalState) => state.entities.users.profiles;

type Props = {
    prefill: CreatePrefill;
    onClose: () => void;
    onShowSidebar: () => void;
};

// CreateTaskModal creates a tix task, optionally from a post, and offers to post its reference back.
export function CreateTaskModal({prefill, onClose, onShowSidebar}: Props) {
    const intl = useIntl();
    const dispatch = useDispatch();
    const currentUserId = useSelector(getCurrentUserId);
    const ids = useSelector((s: GlobalState) => memberIds(s, prefill.channelId));
    const users = useSelector(profiles);
    const titleRef = useRef<HTMLInputElement>(null);

    const [me, setMe] = useState<Me | null>(null);
    const [notLinked, setNotLinked] = useState(false);
    const [projects, setProjects] = useState<Project[] | null>(null);
    const [loadError, setLoadError] = useState('');
    const [project, setProject] = useState(remembered());
    const [title, setTitle] = useState(prefill.title ?? '');
    const [body, setBody] = useState(prefill.body ?? '');
    const [assignee, setAssignee] = useState(currentUserId);
    const [priority, setPriority] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState('');
    const [created, setCreated] = useState<Task | null>(null);
    const [posting, setPosting] = useState(false);
    const [posted, setPosted] = useState(false);

    const load = async () => {
        setLoadError('');
        try {
            const m = await getMe(prefill.channelId);
            setMe(m);
            if (!m.linked) {
                setNotLinked(true);
                return;
            }
            setNotLinked(false);
            const ps = await getProjects();
            setProjects(ps);
            const visible = (key?: string) => ps.find((p) => key && p.key.toLowerCase() === key.toLowerCase())?.key;

            // The channel's default project comes first, then the last one used here, then the first.
            setProject((cur) => visible(m.default_project) ?? visible(cur) ?? ps[0]?.key ?? '');
        } catch (e) {
            if (isNotLinked(e)) {
                setNotLinked(true);
                return;
            }
            setLoadError(errorText(intl, e));
        }
    };

    useEffect(() => {
        load();

        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    const assigneeOptions = useMemo(() => {
        const opts = [
            {value: currentUserId, label: intl.formatMessage({id: 'tix.create.assignMe', defaultMessage: 'Me'})},
            {value: UNASSIGNED, label: intl.formatMessage({id: 'tix.create.unassigned', defaultMessage: 'Nobody'})},
        ];
        const others = ids ? Array.from(ids) : [];
        others.filter((id) => id !== currentUserId).
            map((id) => users[id]).
            filter((u): u is UserProfile => Boolean(u) && !u.is_bot && !u.delete_at).
            slice(0, MAX_MEMBERS).
            forEach((u) => opts.push({value: u.id, label: displayName(u)}));
        return opts;
    }, [currentUserId, ids, users, intl]);

    const priorityOptions = [
        {value: '', label: intl.formatMessage({id: 'tix.create.priorityDefault', defaultMessage: 'Project default'})},
        ...PRIORITIES.map((p) => ({value: String(p), label: intl.formatMessage(priorityMessages[p])})),
    ];

    const submit = async () => {
        if (!title.trim()) {
            setError(intl.formatMessage({id: 'tix.create.titleRequired', defaultMessage: 'Give the task a title.'}));
            titleRef.current?.focus();
            return;
        }
        setSubmitting(true);
        setError('');
        try {
            const t = await createTask({
                project,
                title: title.trim(),
                body,
                priority: priority ? Number(priority) : undefined,
                assignee_user_id: assignee === UNASSIGNED ? undefined : assignee,
            });
            remember(project);
            setCreated(t);
            dispatch(tasksChanged());
        } catch (e) {
            if (isNotLinked(e)) {
                setNotLinked(true);
                return;
            }
            setError(errorText(intl, e));
        } finally {
            setSubmitting(false);
        }
    };

    const postLink = async () => {
        if (!created) {
            return;
        }
        setPosting(true);
        setError('');
        try {
            const message = intl.formatMessage(
                {id: 'tix.create.postMessage', defaultMessage: 'Created tix task {ref}: {title}'},
                {ref: created.ref, title: created.title},
            );
            await postMessage(prefill.channelId, prefill.rootId ?? '', message);
            setPosted(true);
        } catch (e) {
            setError(errorText(intl, e));
        } finally {
            setPosting(false);
        }
    };

    const showInSidebar = () => {
        if (created) {
            dispatch(showTask(created.ref));
            onShowSidebar();
        }
        onClose();
    };

    const modalTitle = intl.formatMessage({id: 'tix.create.title', defaultMessage: 'Create tix task'});
    const cancel = (
        <Button
            emphasis='tertiary'
            onClick={onClose}
        >
            <FormattedMessage
                id='tix.cancel'
                defaultMessage='Cancel'
            />
        </Button>
    );

    if (created) {
        const href = safeHref(created.web_url);
        return (
            <Modal
                title={modalTitle}
                onClose={onClose}
                closeLabel={intl.formatMessage({id: 'tix.close', defaultMessage: 'Close'})}
                footer={
                    <>
                        <Button
                            emphasis='tertiary'
                            onClick={showInSidebar}
                        >
                            <FormattedMessage
                                id='tix.create.showInSidebar'
                                defaultMessage='Show in sidebar'
                            />
                        </Button>
                        <Button
                            onClick={postLink}
                            loading={posting}
                            disabled={posted}
                        >
                            {prefill.postId ? (
                                <FormattedMessage
                                    id='tix.create.postInThread'
                                    defaultMessage='Post link in thread'
                                />
                            ) : (
                                <FormattedMessage
                                    id='tix.create.postInChannel'
                                    defaultMessage='Post link in channel'
                                />
                            )}
                        </Button>
                    </>
                }
            >
                <div
                    className='TixPlugin-CreateTask__done'
                    aria-live='polite'
                >
                    <p>
                        <FormattedMessage
                            id='tix.create.created'
                            defaultMessage='Created {ref}.'
                            values={{
                                ref: href ? (
                                    <ExternalLink
                                        className='TixPlugin-CreateTask__link'
                                        href={href}
                                    >
                                        {created.ref}
                                    </ExternalLink>
                                ) : <strong>{created.ref}</strong>,
                            }}
                        />
                    </p>
                    {posted && (
                        <p>
                            <FormattedMessage
                                id='tix.create.posted'
                                defaultMessage='Posted the link.'
                            />
                        </p>
                    )}
                    {error && <ErrorMessage message={error}/>}
                </div>
            </Modal>
        );
    }

    let content;
    if (loadError) {
        content = (
            <div className='TixPlugin-CreateTask__state'>
                <ErrorMessage message={loadError}/>
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
        );
    } else if (notLinked && me) {
        content = (
            <ConnectPrompt
                canConnect={me.can_connect}
                onConnected={load}
            />
        );
    } else if (!projects) {
        content = (
            <div className='TixPlugin-CreateTask__state'>
                <Spinner
                    size='24'
                    aria-label={intl.formatMessage({id: 'tix.loading', defaultMessage: 'Loading'})}
                />
            </div>
        );
    } else if (projects.length === 0) {
        content = (
            <p>
                <FormattedMessage
                    id='tix.project.none'
                    defaultMessage='You cannot see any tix projects.'
                />
            </p>
        );
    } else {
        content = (
            <form
                className='TixPlugin-CreateTask__form'
                onSubmit={(e) => {
                    e.preventDefault();
                    submit();
                }}
            >
                <Select
                    label={intl.formatMessage({id: 'tix.field.project', defaultMessage: 'Project'})}
                    options={projects.map((p) => ({value: p.key, label: p.name, secondaryLabel: p.key}))}
                    value={project}
                    onChange={setProject}
                />
                <TextInput
                    ref={titleRef}
                    label={intl.formatMessage({id: 'tix.field.title', defaultMessage: 'Title'})}
                    value={title}
                    maxLength={500}
                    invalid={Boolean(error) && !title.trim()}
                    aria-describedby={error ? 'tix-create-error' : undefined}
                    onChange={(e) => setTitle(e.target.value)}
                />
                <TextArea
                    label={intl.formatMessage({id: 'tix.field.description', defaultMessage: 'Description'})}
                    value={body}
                    rows={5}
                    onChange={(e) => setBody(e.target.value)}
                />
                <div className='TixPlugin-CreateTask__row'>
                    <Select
                        label={intl.formatMessage({id: 'tix.field.assignee', defaultMessage: 'Assignee'})}
                        options={assigneeOptions}
                        value={assignee}
                        onChange={setAssignee}
                    />
                    <Select
                        label={intl.formatMessage({id: 'tix.field.priority', defaultMessage: 'Priority'})}
                        options={priorityOptions}
                        value={priority}
                        onChange={setPriority}
                    />
                </div>
                {error && (
                    <div
                        id='tix-create-error'
                        aria-live='assertive'
                    >
                        <ErrorMessage message={error}/>
                    </div>
                )}
            </form>
        );
    }

    const canSubmit = Boolean(projects?.length) && !notLinked && !loadError;
    return (
        <Modal
            title={modalTitle}
            size='medium'
            onClose={onClose}
            closeLabel={intl.formatMessage({id: 'tix.close', defaultMessage: 'Close'})}
            initialFocusRef={titleRef}
            footer={
                <>
                    {cancel}
                    {canSubmit && (
                        <Button
                            onClick={submit}
                            loading={submitting}
                        >
                            <FormattedMessage
                                id='tix.create.submit'
                                defaultMessage='Create task'
                            />
                        </Button>
                    )}
                </>
            }
        >
            {content}
        </Modal>
    );
}

type RootProps = {
    onShowSidebar: () => void;
};

// CreateTaskRoot is registered as a root component and shows the modal while one is requested.
export default function CreateTaskRoot({onShowSidebar}: RootProps) {
    const dispatch = useDispatch();
    const {create} = useSelector(pluginState);
    if (!create) {
        return null;
    }
    return (
        <div className='TixPlugin-CreateTask__backdrop'>
            <CreateTaskModal
                prefill={create}
                onClose={() => dispatch(closeCreate())}
                onShowSidebar={onShowSidebar}
            />
        </div>
    );
}
