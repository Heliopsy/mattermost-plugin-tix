// SPDX-License-Identifier: AGPL-3.0-only

import {screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as client from 'client';

import {CreateTaskModal} from './create_task_modal';

import {CHANNEL, CURRENT_USER, renderWithStore} from '../../../tests/test_utils';

jest.mock('client', () => ({
    ...jest.requireActual('client'),
    getMe: jest.fn(),
    getProjects: jest.fn(),
    createTask: jest.fn(),
    postMessage: jest.fn(),
    connect: jest.fn(),
}));

const mocked = client as jest.Mocked<typeof client>;

const prefill = {
    channelId: CHANNEL,
    postId: 'post1',
    rootId: 'root1',
    title: 'Login is broken',
    body: 'Login is broken\n\n[Mattermost message](http://localhost:8065/team/pl/post1)',
};

describe('CreateTaskModal', () => {
    beforeEach(() => {
        mocked.getMe.mockResolvedValue({linked: true, tix_url: 'https://tix.example.com', can_connect: true});
        mocked.getProjects.mockResolvedValue([{id: 'p1', key: 'infra', name: 'Infra', color: '', workflow_id: 'wf1'}]);
    });

    test('creates a task from a post and posts its link into the thread', async () => {
        mocked.createTask.mockResolvedValue({ref: 'infra-7', title: 'Login is broken', web_url: 'https://tix.example.com/tasks/infra-7'} as never);
        mocked.postMessage.mockResolvedValue({} as never);

        renderWithStore(
            <CreateTaskModal
                prefill={prefill}
                onClose={jest.fn()}
                onShowSidebar={jest.fn()}
            />,
        );

        const title = await screen.findByRole('textbox', {name: 'Title'});
        expect(title).toHaveValue('Login is broken');
        expect(screen.getByRole('textbox', {name: 'Description'})).toHaveValue(prefill.body);

        await userEvent.click(screen.getByRole('button', {name: 'Create task'}));

        await waitFor(() => expect(mocked.createTask).toHaveBeenCalledWith({
            project: 'infra',
            title: 'Login is broken',
            body: prefill.body,
            priority: undefined,
            assignee_user_id: CURRENT_USER,
        }));
        expect(await screen.findByRole('link', {name: 'infra-7'})).toHaveAttribute('href', 'https://tix.example.com/tasks/infra-7');

        await userEvent.click(screen.getByRole('button', {name: 'Post link in thread'}));
        expect(mocked.postMessage).toHaveBeenCalledWith(CHANNEL, 'root1', expect.stringContaining('infra-7'));
        expect(await screen.findByText('Posted the link.')).toBeVisible();
    });

    test('asks for a title before creating', async () => {
        renderWithStore(
            <CreateTaskModal
                prefill={{channelId: CHANNEL}}
                onClose={jest.fn()}
                onShowSidebar={jest.fn()}
            />,
        );
        await screen.findByRole('textbox', {name: 'Title'});
        await userEvent.click(screen.getByRole('button', {name: 'Create task'}));
        expect(await screen.findByText('Give the task a title.')).toBeVisible();
        expect(mocked.createTask).not.toHaveBeenCalled();
    });

    test('shows the connect prompt when the user is not linked', async () => {
        mocked.getMe.mockResolvedValue({linked: false, tix_url: '', can_connect: true});
        renderWithStore(
            <CreateTaskModal
                prefill={prefill}
                onClose={jest.fn()}
                onShowSidebar={jest.fn()}
            />,
        );
        expect(await screen.findByText('Connect to tix')).toBeVisible();
        expect(screen.getByRole('button', {name: 'Connect'})).toBeVisible();
        expect(screen.queryByRole('button', {name: 'Create task'})).not.toBeInTheDocument();
    });
});
