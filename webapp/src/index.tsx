// SPDX-License-Identifier: AGPL-3.0-only

import {pluginURL, setSiteURL} from 'client';
import {messagesFor} from 'i18n';
import manifest from 'manifest';
import {createIntl, FormattedMessage} from 'react-intl';
import reducer, {openCreate} from 'reducer';
import type {Store} from 'redux';

import CheckboxMultipleMarkedOutlineIcon from '@mattermost/compass-icons/components/checkbox-multiple-marked-outline';
import type {Post} from '@mattermost/types/posts';
import type {GlobalState} from '@mattermost/types/store';

import {getCurrentChannelId} from 'mattermost-redux/selectors/entities/common';
import {getConfig} from 'mattermost-redux/selectors/entities/general';
import {getCurrentUserLocale} from 'mattermost-redux/selectors/entities/i18n';
import {getCurrentTeam} from 'mattermost-redux/selectors/entities/teams';

import CreateTaskRoot from 'components/create_task_modal/create_task_modal';
import TixSidebar from 'components/tix_sidebar/tix_sidebar';

import type {PluginRegistry} from 'types/mattermost-webapp';

import '@mattermost/compass-ui/styles';

const MAX_TITLE = 200;

// prefillFromPost turns a post into a task title (its first line) and body (the message and a permalink
// back, its text in the person's language).
export function prefillFromPost(post: Post, permalink: string, linkText: string) {
    const firstLine = post.message.split('\n').map((l) => l.trim()).find(Boolean) ?? '';
    const title = firstLine.length > MAX_TITLE ? `${firstLine.slice(0, MAX_TITLE - 1)}…` : firstLine;
    const body = [post.message.trim(), `[${linkText}](${permalink})`].filter(Boolean).join('\n\n');
    return {
        channelId: post.channel_id,
        postId: post.id,
        rootId: post.root_id || post.id,
        title,
        body,
    };
}

export default class Plugin {
    public async initialize(registry: PluginRegistry, store: Store<GlobalState>) {
        setSiteURL(getConfig(store.getState()).SiteURL);
        registry.registerReducer(reducer);

        const title = (
            <FormattedMessage
                id='tix.sidebar.title'
                defaultMessage='tix'
            />
        );
        const {showRHSPlugin, toggleRHSPlugin} = registry.registerRightHandSidebarComponent(TixSidebar, title);
        const toggle = () => store.dispatch(toggleRHSPlugin);

        registry.registerChannelHeaderButtonAction(
            <CheckboxMultipleMarkedOutlineIcon size={18}/>,
            toggle,
            title,
            (
                <FormattedMessage
                    id='tix.sidebar.tooltip'
                    defaultMessage='tix tasks'
                />
            ),
        );
        registry.registerAppBarComponent({
            iconUrl: pluginURL('/public/app-bar-icon.svg'),
            action: toggle,
            tooltipText: (
                <FormattedMessage
                    id='tix.sidebar.tooltip'
                    defaultMessage='tix tasks'
                />
            ),
            supportedProductIds: null,
        });

        registry.registerRootComponent(() => <CreateTaskRoot onShowSidebar={() => store.dispatch(showRHSPlugin)}/>);

        const createText = (
            <FormattedMessage
                id='tix.create.title'
                defaultMessage='Create tix task'
            />
        );

        // The host passes the post id to both functions, although the registry's types say otherwise.
        const postOf = (arg: unknown): Post | undefined => {
            const id = typeof arg === 'string' ? arg : (arg as Post | undefined)?.id;
            return id ? store.getState().entities.posts.posts[id] : undefined;
        };
        registry.registerPostDropdownMenuAction({
            text: createText,
            action: ((postId: string) => {
                const state = store.getState();
                const post = postOf(postId);
                if (!post) {
                    return;
                }
                const site = (getConfig(state).SiteURL ?? window.location.origin).replace(/\/$/, '');
                const team = getCurrentTeam(state);
                const permalink = `${site}/${team?.name ?? '_redirect'}/pl/${post.id}`;
                const locale = getCurrentUserLocale(state);
                const intl = createIntl({locale, messages: messagesFor(locale), onError: () => null});
                const linkText = intl.formatMessage({id: 'tix.create.fromMessage', defaultMessage: 'Mattermost message'});
                store.dispatch(openCreate(prefillFromPost(post, permalink, linkText)));
            }) as () => void,
            filter: ((arg: unknown) => {
                const post = postOf(arg);
                return Boolean(post) && !post!.type?.startsWith('system_');
            }) as (post: Post) => boolean,
        });

        registry.registerMainMenuAction({
            text: createText,
            action: () => store.dispatch(openCreate({channelId: getCurrentChannelId(store.getState())})),
            mobileIcon: <CheckboxMultipleMarkedOutlineIcon size={16}/>,
        });

        registry.registerTranslations(messagesFor);
    }
}

declare global {
    interface Window {
        registerPlugin(pluginId: string, plugin: Plugin): void;
    }
}

window.registerPlugin(manifest.id, new Plugin());
