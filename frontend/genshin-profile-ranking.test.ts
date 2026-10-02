/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/vue';
import ProfileCards from './ProfileCards.vue';

const mocks = vi.hoisted(() => ({ api: vi.fn(), eligible: true }));
vi.mock('./api.js', () => ({ api: mocks.api }));
const ctx = { user: { id: 'u1', username: 'local', host: null } };
beforeEach(() => {
	vi.clearAllMocks();
	mocks.eligible = true;
	mocks.api.mockImplementation(async (path: string, params: { metric?: string; accountId?: string }) => {
		if (path === 'profiles') return { profiles: [{ linked: true, accountId: 'a1', nickname: 'Traveler', adventureRank: 60, worldLevel: 9, spiralStars: 36, achievements: 100, fetterCount: 10, showcase: [], characters: [] }] };
		if (path === 'rankings') return { entries: mocks.eligible && params.metric === 'achievements' ? [{ accountId: params.accountId, rank: 52 }] : [] };
		throw new Error(`unexpected endpoint ${path}`);
	});
});
afterEach(cleanup);

it('閉じたプロフィールにはリンク・順位を表示せず、開いた対象UIDにだけ表示する', async () => {
	render(ProfileCards, { props: { ctx } });
	const toggle = await screen.findByRole('button', { name: /原神/ });
	expect(screen.queryByRole('link', { name: 'サーバー内の原神ランキング' })).toBeNull();
	await fireEvent.click(toggle);
	await screen.findByText('実績の数 52位');
	expect(screen.getByRole('link', { name: 'サーバー内の原神ランキング' })).toBeTruthy();
	expect(screen.queryByText(/深境螺旋の星の数 .*位/)).toBeNull();
	await fireEvent.click(toggle);
	expect(screen.queryByRole('link', { name: 'サーバー内の原神ランキング' })).toBeNull();
	expect(screen.queryByText('実績の数 52位')).toBeNull();
});

it('ランキング対象外のプロフィールではリンク・順位を表示しない', async () => {
	mocks.eligible = false;
	render(ProfileCards, { props: { ctx } });
	await fireEvent.click(await screen.findByRole('button', { name: /原神/ }));
	await waitFor(() => expect(mocks.api).toHaveBeenCalledTimes(5));
	expect(screen.queryByRole('link', { name: 'サーバー内の原神ランキング' })).toBeNull();
	expect(screen.queryByText(/52位/)).toBeNull();
});

it('リモートプロフィールはローカルランキングへ問い合わせない', async () => {
	render(ProfileCards, { props: { ctx: { user: { ...ctx.user, host: 'remote.example' } } } });
	await fireEvent.click(await screen.findByRole('button', { name: /原神/ }));
	expect(mocks.api).toHaveBeenCalledTimes(1);
	expect(screen.queryByRole('link', { name: 'サーバー内の原神ランキング' })).toBeNull();
});
