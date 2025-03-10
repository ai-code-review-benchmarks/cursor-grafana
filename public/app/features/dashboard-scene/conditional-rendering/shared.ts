import { ConditionalRenderingAfter } from './ConditionalRenderingAfter';
import { ConditionalRenderingBefore } from './ConditionalRenderingBefore';
import { ConditionalRenderingData } from './ConditionalRenderingData';
import { ConditionalRenderingGroup } from './ConditionalRenderingGroup';
import { ConditionalRenderingInterval } from './ConditionalRenderingInterval';
import { ConditionalRenderingVariable } from './ConditionalRenderingVariable';

export type ConditionsRenderingConditions =
  | ConditionalRenderingData
  | ConditionalRenderingAfter
  | ConditionalRenderingBefore
  | ConditionalRenderingVariable
  | ConditionalRenderingInterval
  | ConditionalRenderingGroup;

type NonGroupConditions = Exclude<ConditionsRenderingConditions, ConditionalRenderingGroup>;

export const handleDeleteNonGroupCondition = (model: NonGroupConditions) => {
  if (model.parent instanceof ConditionalRenderingGroup) {
    model.parent.setState({ value: model.parent.state.value.filter((condition) => condition !== model) });
  }
};
