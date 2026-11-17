// SPDX-License-Identifier: AGPL-3.0-only

import type {ReactNode} from 'react';

import './markdown.css';

type PostUtils = {
    formatText: (text: string, options?: Record<string, unknown>) => string;
    messageHtmlToComponent: (html: string, options?: Record<string, unknown>) => ReactNode;
};

declare global {
    interface Window {
        PostUtils?: PostUtils;
    }
}

type Props = {
    text: string;
    className?: string;
};

// Markdown renders through the host's sanitizing formatter, or as plain text when the host offers none.
export default function Markdown({text, className = ''}: Props) {
    const utils = window.PostUtils;
    const classes = `TixPlugin-Markdown ${className}`.trim();
    if (!utils?.formatText || !utils.messageHtmlToComponent) {
        return <div className={`${classes} TixPlugin-Markdown--plain`}>{text}</div>;
    }
    const html = utils.formatText(text, {singleline: false, mentionHighlight: false, atMentions: true});
    return <div className={classes}>{utils.messageHtmlToComponent(html, {mentionHighlight: false})}</div>;
}
