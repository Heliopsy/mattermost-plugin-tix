// SPDX-License-Identifier: AGPL-3.0-only

import {postMessage, PROP_FROM_PLUGIN} from 'client';

import {Client4} from 'mattermost-redux/client';

describe('postMessage', () => {
    it('marks the post as made by the plugin, so thread sync skips it', async () => {
        const spy = jest.spyOn(Client4, 'createPost').mockResolvedValue({} as never);
        await postMessage('ch1', 'root1', 'Created tix task infra-7: Fix it');
        expect(spy).toHaveBeenCalledWith({
            channel_id: 'ch1',
            root_id: 'root1',
            message: 'Created tix task infra-7: Fix it',
            props: {[PROP_FROM_PLUGIN]: 'true'},
        });
    });
});
