// SPDX-License-Identifier: AGPL-3.0-only

import fs from 'fs';
import path from 'path';

import {ApiError} from 'client';
import {errorText, pickMessages} from 'i18n';
import {createIntl} from 'react-intl';

// jest maps i18n JSON imports to a mock, so the catalogs are read from disk here.
const root = path.resolve(__dirname, '..');
const readCatalog = (lang: string): Record<string, string> => JSON.parse(fs.readFileSync(path.join(root, 'i18n', `${lang}.json`), 'utf8'));
const en = readCatalog('en');
const bg = readCatalog('bg');

function sources(dir: string): string[] {
    return fs.readdirSync(dir, {withFileTypes: true}).flatMap((d) => {
        const p = path.join(dir, d.name);
        if (d.isDirectory()) {
            return sources(p);
        }
        return (/\.tsx?$/).test(d.name) && !(/\.test\.tsx?$/).test(d.name) ? [p] : [];
    });
}

// usedMessages finds every message the code names: FormattedMessage id='...' defaultMessage='...',
// and {id: '...', defaultMessage: '...'} descriptors (formatMessage, defineMessages, message tables).
function usedMessages() {
    const out: Array<{id: string; defaultMessage?: string; file: string}> = [];
    const unquote = (s: string) => s.slice(1, -1).replace(/\\'/g, "'");
    for (const file of sources(path.join(root, 'src'))) {
        const text = fs.readFileSync(file, 'utf8');
        const jsx = /id='([^']+)'\s+defaultMessage=('(?:[^'\\]|\\.)*')/g;
        for (const m of text.matchAll(jsx)) {
            out.push({id: m[1], defaultMessage: unquote(m[2]), file});
        }
        const obj = /\{\s*id: '([^']+)',\s*defaultMessage: ('(?:[^'\\]|\\.)*')/g;
        for (const m of text.matchAll(obj)) {
            out.push({id: m[1], defaultMessage: unquote(m[2]), file});
        }
        const bare = /\bid[=:]\s*'(tix\.[^']+)'/g;
        for (const m of text.matchAll(bare)) {
            out.push({id: m[1], file});
        }
    }
    return out;
}

// args are the ICU argument names of a message: {name}, {name, plural, ...}, {name, select, ...}.
function args(message: string) {
    const names = new Set<string>();
    for (const m of message.matchAll(/\{\s*([A-Za-z_][\w]*)\s*[,}]/g)) {
        names.add(m[1]);
    }
    return [...names].sort();
}

describe('message catalogs', () => {
    const used = usedMessages();

    it('finds the messages in the code', () => {
        expect(used.length).toBeGreaterThan(50);
    });

    it('has every id the code uses in en.json, with the same English text', () => {
        const missing = used.filter((u) => !(u.id in en)).map((u) => `${u.id} (${path.relative(root, u.file)})`);
        expect(missing).toEqual([]);
        const differs = used.filter((u) => u.defaultMessage !== undefined && en[u.id] !== undefined && en[u.id] !== u.defaultMessage).
            map((u) => `${u.id}: code "${u.defaultMessage}" vs en.json "${en[u.id]}"`);
        expect(differs).toEqual([]);
    });

    it('has every en.json key in bg.json', () => {
        expect(Object.keys(en).filter((k) => !(k in bg))).toEqual([]);
        const extra = Object.keys(bg).filter((k) => !(k in en));
        if (extra.length) {
            // eslint-disable-next-line no-console
            console.warn(`bg.json has keys en.json does not: ${extra.join(', ')}`);
        }
    });

    it('keeps the same arguments in every language', () => {
        const mismatched = Object.keys(en).filter((k) => k in bg && args(en[k]).join() !== args(bg[k]).join()).
            map((k) => `${k}: en {${args(en[k])}} vs bg {${args(bg[k])}}`);
        expect(mismatched).toEqual([]);
    });

    it('has no empty translations', () => {
        expect(Object.keys(bg).filter((k) => !bg[k].trim())).toEqual([]);
    });
});

describe('pickMessages', () => {
    const catalogs = {en: {a: 'en'}, bg: {a: 'bg'}};
    it.each([
        ['en', 'en'],
        ['en-AU', 'en'],
        ['bg', 'bg'],
        ['bg-BG', 'bg'],
        ['BG', 'bg'],
    ])('picks the catalog for %s', (locale, want) => {
        expect(pickMessages(locale, catalogs).a).toBe(want);
    });
    it('gives nothing for other languages, so defaults show', () => {
        expect(pickMessages('de', catalogs)).toEqual({});
        expect(pickMessages('', catalogs)).toEqual({});
    });
});

describe('errorText', () => {
    const intl = createIntl({locale: 'bg', messages: bg, onError: () => null});
    it('shows the server message as it came', () => {
        expect(errorText(intl, new ApiError('Сървърът каза не.', 400, 'bad_request'))).toBe('Сървърът каза не.');
    });
    it('words a failure without a message in the person language', () => {
        expect(errorText(intl, new ApiError('', 502, ''))).toBe('Заявката не успя (HTTP 502). Опитай пак.');
        expect(errorText(intl, new TypeError('Failed to fetch'))).toBe(bg['tix.error.network']);
    });
});
