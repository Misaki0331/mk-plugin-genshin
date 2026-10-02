/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
export function rankingParams(metric: string, offset: number, scheduleId: number): Record<string, unknown> {
	return { metric, offset, limit: 50, ...(metric === 'stygian' && scheduleId > 0 ? { scheduleId } : {}) };
}

export function resolvedSchedule(current: number, metric: string, received: number): number {
	return metric === 'stygian' ? current || received : 0;
}
export const rankingMetrics = ['spiral', 'achievements', 'friendship', 'stygian'] as const;
export const rankingLabels = {
	spiral: '深境螺旋の星の数',
	achievements: '実績の数',
	friendship: '好感度MAXのキャラクター数',
	stygian: '幽境の激戦',
};
