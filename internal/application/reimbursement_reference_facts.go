package application

import (
	"context"

	"github.com/j-s-te/project-management/internal/domain"
)

type ExpenseTravelRepository interface {
	ReadExpensePreparation(context.Context, string, string, string) (*domain.DeliveryEvent, error)
	ReadExpenseTravelEvent(context.Context, string, string, string) (*domain.DeliveryEvent, error)
}

// expenseTravelFacts 仅为当前费用参与者读取指定服务项准备事实和精确来源链。
// 参数：ctx为请求，tenant/actor为可信边界，p/item为已验证对象，reference为资格，date为费用日。
// 返回值：最小权威事实；依赖失败返回错误，缺少计划或来源保持UNKNOWN。
func (s *Service) expenseTravelFacts(ctx context.Context, tenant, actor string, p domain.Project, item *domain.ServiceItem, reference domain.ExpenseProjectReference, date string) (domain.ExpenseTravelSnapshot, error) {
	if !reference.ParticipantEligible {
		return domain.ExpenseTravelUnknown("ACTOR_NOT_ELIGIBLE"), nil
	}
	if item == nil {
		return domain.ExpenseTravelUnknown("TRAVEL_SERVICE_ITEM_REQUIRED"), nil
	}
	repo, ok := s.Repo.(ExpenseTravelRepository)
	if !ok {
		return domain.ExpenseTravelSnapshot{}, ErrServiceTimeout
	}
	current, err := repo.ReadExpensePreparation(ctx, tenant, p.ID, item.ID)
	if err != nil {
		return domain.ExpenseTravelSnapshot{}, err
	}
	sources, err := expenseTravelSourceChain(ctx, repo, tenant, p.ID, current)
	if err != nil {
		return domain.ExpenseTravelSnapshot{}, err
	}
	return domain.ExpenseTravelFacts(p, item, actor, date, current, sources), nil
}

// expenseTravelSourceChain 按显式事件ID读取有界来源链，不扫描或猜测其他项目行程。
// 参数：ctx为上下文，repo为权威只读仓储，tenant/project为可信边界，current为当前准备事实。
// 返回值：最多16层来源事实；缺失留给领域标UNKNOWN，读取故障返回错误。
func expenseTravelSourceChain(ctx context.Context, repo ExpenseTravelRepository, tenant, project string, current *domain.DeliveryEvent) (map[string]domain.DeliveryEvent, error) {
	sources := map[string]domain.DeliveryEvent{}
	if current != nil {
		sources[current.ID] = *current
	}
	seen := map[string]bool{}
	event := current
	for depth := 0; depth < domain.MaxExpenseTravelSourceDepth && event != nil; depth++ {
		seen[event.ID] = true
		travel, valid := domain.ExpenseTravelArrangement(*event)
		if !valid || travel.Mode != domain.TravelModeExisting || travel.ReferenceEventID == "" || len(travel.ReferenceEventID) > 128 || seen[travel.ReferenceEventID] {
			break
		}
		source, err := repo.ReadExpenseTravelEvent(ctx, tenant, project, travel.ReferenceEventID)
		if err != nil {
			return nil, err
		}
		if source == nil {
			break
		}
		if source.ID != travel.ReferenceEventID {
			return nil, ErrForbidden
		}
		sources[source.ID] = *source
		event = source
	}
	return sources, nil
}
