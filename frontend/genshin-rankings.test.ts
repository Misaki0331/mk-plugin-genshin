/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/vue';
import { difficultyImage, difficultyLabel } from './stygian-difficulty.js';
import Rankings from './Rankings.vue';

const mocks = vi.hoisted(() => ({ api: vi.fn(), getUsers: vi.fn(), definePage: vi.fn(), metric: 'spiral' }));
vi.mock('./api.js', () => ({ api: mocks.api }));
vi.mock('@/plugin-api.js', async () => {
	const { defineComponent, h, ref } = await import('vue');
	const wrapper = defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()); } });
	return {
		PageWithHeader: wrapper,
		MkButton: wrapper,
		MkLoading: wrapper,
		MkSelect: wrapper,
		MkSwitch: defineComponent({
			props: { modelValue: Boolean },
			emits: ['update:modelValue'],
			setup(props, { emit }) {
				return () => h('input', { type: 'checkbox', 'aria-label': '公開UIDを表示する', checked: props.modelValue, onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).checked) });
			},
		}),
		MkAvatar: defineComponent({ props: ['user'], setup(props) { return () => h('img', { src: props.user.avatarUrl, alt: 'ユーザーアイコン' }); } }),
		MkUserName: defineComponent({ props: ['user'], setup(props) { return () => h('span', props.user.name); } }),
		MkTime: defineComponent({ props: ['time'], setup(props) { return () => h('time', { 'data-time': props.time }, '2時間前'); } }),
		useMkSelect: (options: { items: unknown[]; initialValue: string }) => ({ model: ref(mocks.metric || options.initialValue), def: options.items }),
		getUsers: mocks.getUsers,
		definePage: mocks.definePage,
	};
});

beforeEach(() => {
	vi.clearAllMocks();
	mocks.metric = 'spiral';
	mocks.api.mockResolvedValue({ metric: 'spiral', scheduleId: 0, hasMore: false, entries: [
		{ rank: 1, userId: 'u1', accountId: 'a1', uid: '800000001', nickname: 'Traveler', value: 36, fetchedAt: '2026-10-01T00:00:00Z' },
		{ rank: 2, userId: 'u1', accountId: 'a2', nickname: 'Private traveler', value: 33, fetchedAt: '2026-10-01T00:00:00Z' },
	] });
	mocks.getUsers.mockResolvedValue([{ id: 'u1', name: 'Misskey user', avatarUrl: '/avatar.png' }]);
});
afterEach(cleanup);

describe('原神ランキングの標準UI', () => {
	it('上位3位はメダル絵文字、4位以降は数値で表示する', async () => {
		mocks.api.mockResolvedValue({ metric: 'spiral', scheduleId: 0, hasMore: false, entries: [1, 2, 3, 4].map(rank => ({ rank, userId: 'u1', accountId: `a${rank}`, nickname: `Traveler ${rank}`, value: 36, fetchedAt: '2026-10-01T00:00:00Z' })) });
		render(Rankings);
		await screen.findByText('Traveler 1');
		['🥇', '🥈', '🥉', '4'].forEach((label, index) => {
			expect(screen.getByLabelText(`${index + 1}位`).textContent).toBe(label);
			expect(screen.getByLabelText(`${index + 1}位`).className.includes('medal')).toBe(index < 3);
		});
	});
	it('初期選択は幽境の激戦', async () => {
		mocks.metric = '';
		render(Rankings);
		await screen.findByText('Traveler');
		expect(mocks.api).toHaveBeenCalledWith('rankings', { metric: 'stygian', offset: 0, limit: 50 });
		expect(screen.getByText('本人確認済みのUIDごとに集計します。設定のプロフィール内の「原神」からランキング参加を無効にできます。')).toBeTruthy();
		expect(screen.queryByRole('tooltip')).toBeNull();
	});
	it('幽境は開催IDだけを注記し、難易度アイコンのホバー名を表示する', async () => {
		mocks.metric = 'stygian';
		mocks.api.mockResolvedValue({ metric: 'stygian', scheduleId: 123, hasMore: false, entries: [{ rank: 1, userId: 'u1', accountId: 'a1', nickname: 'Traveler', difficulty: 6, seconds: 42, fetchedAt: '2026-10-01T00:00:00Z' }] });
		render(Rankings);
		await screen.findByText('開催ID 123');
		expect(screen.getByRole('img', { name: 'アルティメット' }).getAttribute('title')).toBe('アルティメット');
		expect(screen.getByRole('img', { name: 'アルティメット' }).getAttribute('src')).toBe('/api/plugin/genshin/asset/UI_LeyLineChallenge_Medal_6.png');
		expect(screen.getByRole('img', { name: 'アルティメット' }).parentElement?.textContent).toBe(' 42 秒');
		expect(screen.queryByText(/難易度が高い順/)).toBeNull();
		expect(screen.queryByText(/公開設定で許可されたUIDだけ/)).toBeNull();
	});
	it('難易度1から6を指定された名称とアイコンへ対応させる', () => {
		['イージー', 'ノーマル', 'ハード', 'マスター', 'エクストラ', 'アルティメット'].forEach((label, index) => {
			expect(difficultyLabel(index + 1)).toBe(label);
			expect(difficultyImage(index + 1)).toBe(`/api/plugin/genshin/asset/UI_LeyLineChallenge_Medal_${index + 1}.png`);
		});
		expect(difficultyLabel(0)).toBe('難易度不明');
		expect(difficultyImage(7)).toBeUndefined();
		expect(difficultyImage(0)).toBeUndefined();
		expect(difficultyImage(1.5)).toBeUndefined();
	});
	it('UIDは既定で隠し、ユーザーアイコンと相対時刻を表示する', async () => {
		render(Rankings);
		await screen.findByText('Traveler');
		expect(screen.queryByText(/800000001/)).toBeNull();
		expect(screen.getAllByAltText('ユーザーアイコン')[0].getAttribute('src')).toBe('/avatar.png');
		expect(screen.getAllByText('2時間前')).toHaveLength(2);
		expect(mocks.getUsers).toHaveBeenCalledOnce();
		expect(mocks.getUsers).toHaveBeenCalledWith(['u1']);
	});
	it('表示を有効にしても公開許可されたUIDだけを表示する', async () => {
		render(Rankings);
		await screen.findByText('Traveler');
		await fireEvent.click(screen.getByRole('checkbox', { name: '公開UIDを表示する' }));
		expect(screen.getByText(/UID 800000001/)).toBeTruthy();
		expect(screen.queryByText(/UID undefined/)).toBeNull();
	});
	it('見つけるへ埋め込む場合はページヘッダーを上書きしない', async () => {
		render(Rankings, { props: { embedded: true } });
		await screen.findByText('Traveler');
		expect(mocks.definePage).not.toHaveBeenCalled();
	});
	it('ユーザー情報の取得失敗でもランキングを表示する', async () => {
		mocks.getUsers.mockRejectedValue(new Error('temporary failure'));
		render(Rankings);
		await screen.findByText('Traveler');
		expect(screen.getByText('36')).toBeTruthy();
	});
});
