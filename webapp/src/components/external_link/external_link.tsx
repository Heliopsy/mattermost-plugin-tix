// SPDX-License-Identifier: AGPL-3.0-only

import type {ReactNode} from 'react';

type Props = {
    href: string;
    className?: string;
    children: ReactNode;
};

// ExternalLink opens a link to tix in a new tab without giving it a handle on this window.
// The host's ExternalLink isn't exposed to plugins, so this stands in for it.
export default function ExternalLink({href, className, children}: Props) {
    return (
        // eslint-disable-next-line @mattermost/use-external-link
        <a
            className={className}
            href={href}
            target='_blank'
            rel='noopener noreferrer'
        >
            {children}
        </a>
    );
}
