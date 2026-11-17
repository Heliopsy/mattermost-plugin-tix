// SPDX-License-Identifier: AGPL-3.0-only

import {getMe, getProjects, isNotLinked} from 'client';
import {useCallback, useEffect, useMemo, useState} from 'react';
import {FormattedMessage, useIntl} from 'react-intl';
import {useDispatch, useSelector} from 'react-redux';
import {openCreate, pluginState, showTask, tasksChanged} from 'reducer';

import PlusIcon from '@mattermost/compass-icons/components/plus';
import {Button} from '@mattermost/compass-ui/components/button';
import {ErrorMessage} from '@mattermost/compass-ui/components/error-message';
import {Icon} from '@mattermost/compass-ui/components/icon';
import {SearchInput} from '@mattermost/compass-ui/components/search-input';
import {Select} from '@mattermost/compass-ui/components/select';
import {Spinner} from '@mattermost/compass-ui/components/spinner';
import {Tabs} from '@mattermost/compass-ui/components/tabs';

import {getCurrentChannelId} from 'mattermost-redux/selectors/entities/common';

import ConnectPrompt from 'components/connect_prompt/connect_prompt';

import type {Me, Project} from 'types/tix';

import TaskDetail from './task_detail';
import TaskList from './task_list';
import useTasks from './use_tasks';

import './tix_sidebar.css';

type Tab = 'mine' | 'project';

const PROJECT_KEY = 'tix.sidebar.project';

function remembered(key: string) {
    try {
        return window.localStorage.getItem(key) ?? '';
    } catch {
        return '';
    }
}

function remember(key: string, value: string) {
    try {
        window.localStorage.setItem(key, value);
    } catch {
        // Storage can be off; the choice just isn't kept.
    }
}

function useDebounced<T>(value: T, ms: number) {
    const [v, setV] = useState(value);
    useEffect(() => {
        const t = setTimeout(() => setV(value), ms);
        return () => clearTimeout(t);
    }, [value, ms]);
    return v;
}

type ListProps = {
    refresh: number;
    onOpen: (ref: string) => void;
    onNotLinked: () => void;
};

function MineTab({refresh, onOpen, onNotLinked}: ListProps) {
    const filter = useMemo(() => ({mine: true, open: true, limit: 100}), []);
    const tasks = useTasks(filter, refresh, onNotLinked);
    return (
        <TaskList
            tasks={tasks}
            grouped={true}
            onOpen={onOpen}
            empty={
                <FormattedMessage
                    id='tix.mine.empty'
                    defaultMessage='No open tasks are assigned to you.'
                />
            }
        />
    );
}

function ProjectTab({refresh, onOpen, onNotLinked}: ListProps) {
    const intl = useIntl();
    const [projects, setProjects] = useState<Project[] | null>(null);
    const [error, setError] = useState('');
    const [project, setProject] = useState(remembered(PROJECT_KEY));
    const [search, setSearch] = useState('');
    const q = useDebounced(search.trim(), 300);

    useEffect(() => {
        getProjects().then((ps) => {
            setProjects(ps);
            setProject((cur) => (ps.some((p) => p.key === cur) ? cur : ps[0]?.key ?? ''));
        }).catch((e) => {
            if (isNotLinked(e)) {
                onNotLinked();
                return;
            }
            setError((e as Error).message);
        });
    }, [onNotLinked]);

    const filter = useMemo(() => ({project, q, open: true}), [project, q]);
    const tasks = useTasks(filter, refresh, onNotLinked, !project);

    if (error) {
        return <ErrorMessage message={error}/>;
    }
    if (!projects) {
        return (
            <div className='TixPlugin-TaskList__state'>
                <Spinner
                    size='24'
                    aria-label={intl.formatMessage({id: 'tix.project.loading', defaultMessage: 'Loading projects'})}
                />
            </div>
        );
    }
    if (projects.length === 0) {
        return (
            <p className='TixPlugin-Sidebar__note'>
                <FormattedMessage
                    id='tix.project.none'
                    defaultMessage='You cannot see any tix projects.'
                />
            </p>
        );
    }

    return (
        <>
            <div className='TixPlugin-Sidebar__filters'>
                <Select
                    size='small'
                    aria-label={intl.formatMessage({id: 'tix.field.project', defaultMessage: 'Project'})}
                    options={projects.map((p) => ({value: p.key, label: p.name, secondaryLabel: p.key}))}
                    value={project}
                    onChange={(v) => {
                        setProject(v);
                        remember(PROJECT_KEY, v);
                    }}
                />
                <SearchInput
                    size='small'
                    aria-label={intl.formatMessage({id: 'tix.project.search', defaultMessage: 'Search tasks'})}
                    placeholder={intl.formatMessage({id: 'tix.project.search', defaultMessage: 'Search tasks'})}
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                    onClear={() => setSearch('')}
                    clearLabel={intl.formatMessage({id: 'tix.project.clear', defaultMessage: 'Clear search'})}
                />
            </div>
            <TaskList
                tasks={tasks}
                onOpen={onOpen}
                empty={q ? (
                    <FormattedMessage
                        id='tix.project.noMatch'
                        defaultMessage='No open tasks match.'
                    />
                ) : (
                    <FormattedMessage
                        id='tix.project.empty'
                        defaultMessage='This project has no open tasks.'
                    />
                )}
            />
        </>
    );
}

// TixSidebar is the right-hand sidebar: the user's tasks, a project's tasks and one task in detail.
export default function TixSidebar() {
    const intl = useIntl();
    const dispatch = useDispatch();
    const {openRef, refresh} = useSelector(pluginState);
    const channelId = useSelector(getCurrentChannelId);
    const [me, setMe] = useState<Me | null>(null);
    const [meError, setMeError] = useState('');
    const [notLinked, setNotLinked] = useState(false);
    const [tab, setTab] = useState<Tab>('mine');
    const [selected, setSelected] = useState<string | null>(null);

    const loadMe = useCallback(async () => {
        setMeError('');
        try {
            const m = await getMe();
            setMe(m);
            setNotLinked(!m.linked);
        } catch (e) {
            setMeError((e as Error).message);
        }
    }, []);

    useEffect(() => {
        loadMe();
    }, [loadMe]);

    // Another part of the plugin (the create modal) asked to show a task.
    useEffect(() => {
        if (openRef) {
            setSelected(openRef);
            dispatch(showTask(null));
        }
    }, [openRef, dispatch]);

    const onNotLinked = useCallback(() => setNotLinked(true), []);
    const onChanged = useCallback(() => dispatch(tasksChanged()), [dispatch]);

    if (meError) {
        return (
            <div className='TixPlugin-Sidebar'>
                <div className='TixPlugin-TaskList__state'>
                    <ErrorMessage message={meError}/>
                    <Button
                        emphasis='tertiary'
                        size='small'
                        onClick={loadMe}
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
    if (!me) {
        return (
            <div className='TixPlugin-Sidebar'>
                <div className='TixPlugin-TaskList__state'>
                    <Spinner
                        size='24'
                        aria-label={intl.formatMessage({id: 'tix.loading', defaultMessage: 'Loading'})}
                    />
                </div>
            </div>
        );
    }
    if (notLinked) {
        return (
            <div className='TixPlugin-Sidebar'>
                <ConnectPrompt
                    canConnect={me.can_connect}
                    onConnected={loadMe}
                />
            </div>
        );
    }
    if (selected) {
        return (
            <div className='TixPlugin-Sidebar'>
                <TaskDetail
                    key={selected}
                    taskRef={selected}
                    onBack={() => setSelected(null)}
                    onChanged={onChanged}
                    onNotLinked={onNotLinked}
                />
            </div>
        );
    }

    const tabs = [
        {key: 'mine', id: 'tix-tab-mine', panelId: 'tix-panel', label: intl.formatMessage({id: 'tix.tab.mine', defaultMessage: 'Mine'})},
        {key: 'project', id: 'tix-tab-project', panelId: 'tix-panel', label: intl.formatMessage({id: 'tix.tab.project', defaultMessage: 'Project'})},
    ];

    return (
        <div className='TixPlugin-Sidebar'>
            <div className='TixPlugin-Sidebar__header'>
                <Tabs
                    tabs={tabs}
                    activeKey={tab}
                    onChange={(k) => setTab(k as Tab)}
                    appearance='underlined'
                    controls={
                        <Button
                            size='x-small'
                            emphasis='tertiary'
                            leadingIcon={<Icon glyph={<PlusIcon/>}/>}
                            onClick={() => dispatch(openCreate({channelId}))}
                        >
                            <FormattedMessage
                                id='tix.create.short'
                                defaultMessage='New task'
                            />
                        </Button>
                    }
                />
            </div>
            <div
                id='tix-panel'
                role='tabpanel'
                aria-labelledby={tab === 'mine' ? 'tix-tab-mine' : 'tix-tab-project'}
                className='TixPlugin-Sidebar__body'
            >
                {tab === 'mine' ? (
                    <MineTab
                        refresh={refresh}
                        onOpen={setSelected}
                        onNotLinked={onNotLinked}
                    />
                ) : (
                    <ProjectTab
                        refresh={refresh}
                        onOpen={setSelected}
                        onNotLinked={onNotLinked}
                    />
                )}
            </div>
        </div>
    );
}
