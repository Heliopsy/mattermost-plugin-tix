// SPDX-License-Identifier: AGPL-3.0-only

import {screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as client from 'client';

import type {Task} from 'types/tix';

import TixSidebar from './tix_sidebar';

import {renderWithStore} from '../../../tests/test_utils';

jest.mock('client', () => ({
    ...jest.requireActual('client'),
    getMe: jest.fn(),
    listTasks: jest.fn(),
    getTask: jest.fn(),
    getProjects: jest.fn(),
}));

const mocked = client as jest.Mocked<typeof client>;

const task = (ref: string, label: string, category: Task['state']['category']): Task => ({
    id: ref,
    ref,
    title: `Title of ${ref}`,
    body: '',
    status: label.toLowerCase(),
    priority: 2,
    project_id: 'p1',
    project_key: 'infra',
    project_name: 'Infra',
    tags: [],
    blocked: false,
    created_at: '',
    updated_at: '',
    version: 1,
    web_url: `https://tix.example.com/tasks/${ref}`,
    state: {key: label.toLowerCase(), label, category, terminal: false},
    assignee: {actor_id: 'a1', name: 'Alice', user_id: 'u1', username: 'alice'},
});

describe('TixSidebar', () => {
    beforeEach(() => {
        mocked.getMe.mockResolvedValue({linked: true, tix_url: 'https://tix.example.com', can_connect: false});
    });

    test('lists my open tasks grouped by status category and opens one', async () => {
        mocked.listTasks.mockResolvedValue({items: [task('infra-1', 'Todo', 'todo'), task('infra-2', 'Doing', 'in_progress')], next_cursor: ''});
        mocked.getTask.mockResolvedValue({
            task: {...task('infra-2', 'Doing', 'in_progress'), body: 'Fix it'},
            comments: [],
            transitions: [{to: 'done', label: 'Done', category: 'done', requires_comment: true}],
        });

        renderWithStore(<TixSidebar/>);

        const inProgress = await screen.findByRole('heading', {name: /In progress/});
        expect(screen.getByRole('heading', {name: /To do/})).toBeVisible();
        expect(mocked.listTasks).toHaveBeenCalledWith(expect.objectContaining({mine: true, open: true}));

        // In progress comes before To do.
        const headings = screen.getAllByRole('heading', {level: 3});
        expect(headings[0]).toBe(inProgress);

        await userEvent.click(screen.getByRole('button', {name: /infra-2/}));
        expect(await screen.findByRole('heading', {name: 'Title of infra-2', level: 2})).toBeVisible();
        expect(mocked.getTask).toHaveBeenCalledWith('infra-2');

        // A move that requires a comment asks for one first.
        const moves = screen.getByRole('heading', {name: 'Move to'}).parentElement!;
        await userEvent.click(within(moves).getByRole('button', {name: 'Done'}));
        expect(screen.getByRole('textbox', {name: 'Moving to Done needs a comment'})).toBeVisible();
        expect(screen.getByRole('button', {name: 'Move to Done'})).toBeDisabled();
    });

    test('shows an empty state', async () => {
        mocked.listTasks.mockResolvedValue({items: [], next_cursor: ''});
        renderWithStore(<TixSidebar/>);
        expect(await screen.findByText('No open tasks are assigned to you.')).toBeVisible();
    });

    test('shows the connect prompt for an unlinked user', async () => {
        mocked.getMe.mockResolvedValue({linked: false, tix_url: '', can_connect: false});
        renderWithStore(<TixSidebar/>);
        expect(await screen.findByText('Connect to tix')).toBeVisible();
        expect(screen.getByText(/Run \/tix connect/)).toBeVisible();
    });

    test('shows the connect prompt when tix rejects the token', async () => {
        mocked.listTasks.mockRejectedValue(new client.ApiError('rejected', 409, 'token_rejected'));
        renderWithStore(<TixSidebar/>);
        expect(await screen.findByText('Connect to tix')).toBeVisible();
    });
});
