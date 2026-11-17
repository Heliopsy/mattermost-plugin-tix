// SPDX-License-Identifier: AGPL-3.0-only

import {render} from '@testing-library/react';
import type {ReactElement} from 'react';
import {IntlProvider} from 'react-intl';
import {Provider} from 'react-redux';
import reducer from 'reducer';
import {combineReducers, createStore} from 'redux';

export const CURRENT_USER = 'currentuserid000000000000a';
export const CHANNEL = 'channelid0000000000000000a';

// renderWithStore renders ui against a real Redux store holding the host entities the plugin reads.
export function renderWithStore(ui: ReactElement, entities: Record<string, unknown> = {}) {
    const base = {
        users: {currentUserId: CURRENT_USER, profiles: {}, profilesInChannel: {}},
        channels: {currentChannelId: CHANNEL},
        teams: {currentTeamId: 'team'},
        general: {config: {}},
        ...entities,
    };
    const store = createStore(combineReducers({
        entities: (s = base) => s,
        'plugins-cc.heliopsy.tix': reducer,
    }));
    const result = render(
        <Provider store={store}>
            <IntlProvider
                locale='en'
                onError={() => null}
            >
                {ui}
            </IntlProvider>
        </Provider>,
    );
    return {...result, store};
}
