// SPDX-License-Identifier: AGPL-3.0-only

import {ApiError} from 'client';
import type {IntlShape} from 'react-intl';

import bg from '../i18n/bg.json';
import en from '../i18n/en.json';

export type Catalog = Record<string, string>;

// pickMessages returns the catalog for a Mattermost locale: English for en and en-*, Bulgarian for
// bg and bg-*, and nothing otherwise, so react-intl falls back to each message's English default.
export function pickMessages(locale: string, catalogs: Record<string, Catalog>): Catalog {
    const lang = (locale || '').toLowerCase().split(/[-_]/)[0];
    return catalogs[lang] ?? {};
}

export const messagesFor = (locale: string) => pickMessages(locale, {en, bg});

// errorText is the line shown for a failed request. The plugin server already answers in the
// person's language; only failures without a server message are worded here.
export function errorText(intl: IntlShape, e: unknown) {
    if (e instanceof ApiError) {
        return e.message || intl.formatMessage(
            {id: 'tix.error.requestFailed', defaultMessage: 'The request failed (HTTP {status}). Try again.'},
            {status: e.status},
        );
    }
    if (e instanceof TypeError || !(e instanceof Error) || !e.message) {
        return intl.formatMessage({id: 'tix.error.network', defaultMessage: 'Could not reach the server. Check your connection and try again.'});
    }
    return e.message;
}
