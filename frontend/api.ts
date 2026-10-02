/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

/*
 * バックエンド呼び出しの薄いラッパ。
 *
 * host.api は POST 固定 (misskeyApi と同じ) なので、バックエンド側も POST で
 * 揃えてある。画像だけは <img> が GET しか出せないため GET。
 */

let call: <T>(endpoint: string, params?: Record<string, unknown>) => Promise<T>;

/** Called once from the plugin's setup. */
export function initApi(fn: typeof call): void {
	call = fn;
}

export function api<T>(path: string, params: Record<string, unknown> = {}): Promise<T> {
	return call<T>(`plugin/genshin/${path}`, params);
}

export type LinkChallenge = { uid: string; code: string; expiresAt: string; nextCheckAt: string; attempts: number };
export type MeResponse = { uids: string[]; limit: number; pending: LinkChallenge | null };
export type Preferences = { publishUid: boolean; publishSignature: boolean; rankingEnabled: boolean };
export type RankingResponse = { metric: string; scheduleId: number; hasMore: boolean; entries: { rank: number; userId: string; accountId: string; uid?: string; nickname: string; value: number; difficulty?: number; seconds?: number; fetchedAt: string }[] };
export type VerifyResponse = { verified: boolean; uid?: string; nextCheckAt?: string; expiresAt?: string };

export type ShowcaseCharacter = {
	avatarId: number;
	level: number;
	element: string;
	icon: string;
};

/** ラベル付きの数値。percent が true なら "%" を付けて表示する。 */
export type Stat = {
	label: string;
	value: number;
	percent: boolean;
};

export type Talent = {
	icon: string;
	level: number;
	/** 命ノ星座による加算 (0 か 3)。 */
	extra: number;
};

export type Weapon = {
	name: string;
	icon: string;
	rarity: number;
	level: number;
	refine: number;
	stats: Stat[];
};

export type Artifact = {
	slot: string;
	name: string;
	setName: string;
	icon: string;
	rarity: number;
	level: number;
	main: Stat;
	subs: Stat[];
};

/** ショーケースに飾られたキャラのビルド一式。 */
export type BuildCharacter = {
	avatarId: number;
	name: string;
	icon: string;
	element: string;
	level: number;
	ascension: number;
	constellation: number;
	constIcons: string[];
	friendship: number;
	talents: Talent[];
	weapon?: Weapon;
	artifacts: Artifact[];
	stats: Stat[];
};

export type LinkedProfile = {
	linked: true;
	uid?: string;
	accountId?: string;
	nickname: string;
	adventureRank: number;
	worldLevel: number;
	signature?: string;
	region: string;
	achievements: number;
	spiral: string;
	spiralStars: number;
	theater: string;
	theaterStars: number;
	fetterCount: number;
	profileIcon: string;
	nameCard: string;
	showcase: ShowcaseCharacter[];
	characters: BuildCharacter[];
	fetchedAt: string;
};

export type ProfileResponse = { linked: false } | LinkedProfile;
