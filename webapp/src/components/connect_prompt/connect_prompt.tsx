// SPDX-License-Identifier: AGPL-3.0-only

import {connect} from 'client';
import {errorText} from 'i18n';
import {useState} from 'react';
import {FormattedMessage, useIntl} from 'react-intl';

import {SectionNotice} from '@mattermost/compass-ui/components/section-notice';

import './connect_prompt.css';

type Props = {
    canConnect: boolean;
    onConnected: () => void;
};

// ConnectPrompt is shown to someone who has not connected Mattermost to tix yet.
export default function ConnectPrompt({canConnect, onConnected}: Props) {
    const intl = useIntl();
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');

    const run = async () => {
        setBusy(true);
        setError('');
        try {
            await connect();
            onConnected();
        } catch (e) {
            setError(errorText(intl, e));
        } finally {
            setBusy(false);
        }
    };

    return (
        <div
            className='TixPlugin-ConnectPrompt'
            aria-live='polite'
        >
            <SectionNotice
                type='info'
                title={
                    <FormattedMessage
                        id='tix.connect.title'
                        defaultMessage='Connect to tix'
                    />
                }
                description={
                    <>
                        {canConnect ? (
                            <FormattedMessage
                                id='tix.connect.byEmail'
                                defaultMessage='Connect finds your tix account by your verified Mattermost email and acts as you in tix. You can also run /tix connect in any channel.'
                            />
                        ) : (
                            <FormattedMessage
                                id='tix.connect.byCommand'
                                defaultMessage='Run /tix connect in any channel and paste a token you made in tix.'
                            />
                        )}
                        {error && <p className='TixPlugin-ConnectPrompt__error'>{error}</p>}
                    </>
                }
                primaryButtonLabel={canConnect ? (
                    <FormattedMessage
                        id='tix.connect.button'
                        defaultMessage='Connect'
                    />
                ) : undefined}
                onPrimaryAction={canConnect ? run : undefined}
                primaryActionLoading={busy}
            />
        </div>
    );
}
