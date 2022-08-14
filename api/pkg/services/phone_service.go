package services

import (
	"context"
	"fmt"
	"time"

	"github.com/NdoleStudio/httpsms/pkg/events"
	cloudevents "github.com/cloudevents/sdk-go/v2"

	"github.com/google/uuid"
	"github.com/nyaruka/phonenumbers"

	"github.com/NdoleStudio/httpsms/pkg/repositories"
	"github.com/palantir/stacktrace"

	"github.com/NdoleStudio/httpsms/pkg/entities"
	"github.com/NdoleStudio/httpsms/pkg/telemetry"
)

// PhoneService is handles phone requests
type PhoneService struct {
	service
	logger     telemetry.Logger
	tracer     telemetry.Tracer
	repository repositories.PhoneRepository
	dispatcher *EventDispatcher
}

// NewPhoneService creates a new PhoneService
func NewPhoneService(
	logger telemetry.Logger,
	tracer telemetry.Tracer,
	repository repositories.PhoneRepository,
	dispatcher *EventDispatcher,
) (s *PhoneService) {
	return &PhoneService{
		logger:     logger.WithService(fmt.Sprintf("%T", s)),
		tracer:     tracer,
		dispatcher: dispatcher,
		repository: repository,
	}
}

// Index fetches the heartbeats for a phone number
func (service *PhoneService) Index(ctx context.Context, authUser entities.AuthUser, params repositories.IndexParams) (*[]entities.Phone, error) {
	ctx, span := service.tracer.Start(ctx)
	defer span.End()

	ctxLogger := service.tracer.CtxLogger(service.logger, span)

	phones, err := service.repository.Index(ctx, authUser.ID, params)
	if err != nil {
		msg := fmt.Sprintf("could not fetch phones with parms [%+#v]", params)
		return nil, service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	ctxLogger.Info(fmt.Sprintf("fetched [%d] phones with prams [%+#v]", len(*phones), params))
	return phones, nil
}

// PhoneUpsertParams are parameters for creating a new entities.Phone
type PhoneUpsertParams struct {
	PhoneNumber               phonenumbers.PhoneNumber
	FcmToken                  *string
	MessagesPerMinute         *uint
	MessageExpirationDuration *time.Duration
	Source                    string
	UserID                    entities.UserID
}

// Upsert a new entities.Phone
func (service *PhoneService) Upsert(ctx context.Context, params PhoneUpsertParams) (*entities.Phone, error) {
	ctx, span := service.tracer.Start(ctx)
	defer span.End()

	ctxLogger := service.tracer.CtxLogger(service.logger, span)

	phone, err := service.repository.Load(ctx, params.UserID, phonenumbers.Format(&params.PhoneNumber, phonenumbers.E164))
	if stacktrace.GetCode(err) == repositories.ErrCodeNotFound {
		return service.createPhone(ctx, params)
	}

	if err != nil {
		msg := fmt.Sprintf("cannot upsert phone with id [%s] and number [%s]", phone.ID, phone.PhoneNumber)
		return nil, service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	if err = service.repository.Save(ctx, service.update(phone, params)); err != nil {
		msg := fmt.Sprintf("cannot update phone with id [%s] and number [%s]", phone.ID, phone.PhoneNumber)
		return nil, service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}
	ctxLogger.Info(fmt.Sprintf("phone saved with id [%s] in the phone repository", phone.ID))

	event, err := service.createPhoneUpdatedEvent(params.Source, events.PhoneUpdatedPayload{
		PhoneID:   phone.ID,
		UserID:    phone.UserID,
		Timestamp: phone.UpdatedAt,
		Owner:     phone.PhoneNumber,
	})
	if err != nil {
		msg := fmt.Sprintf("cannot create event when phone is updated")
		return nil, service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	if err = service.dispatcher.Dispatch(ctx, event); err != nil {
		msg := fmt.Sprintf("cannot dispatch event [%s] for phone with id [%s]", event.Type(), phone.ID)
		return nil, service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	return phone, nil
}

// Delete an entities.Phone
func (service *PhoneService) Delete(ctx context.Context, source string, userID entities.UserID, phoneID uuid.UUID) error {
	ctx, span := service.tracer.Start(ctx)
	defer span.End()

	ctxLogger := service.tracer.CtxLogger(service.logger, span)

	phone, err := service.repository.LoadByID(ctx, userID, phoneID)
	if err != nil {
		msg := fmt.Sprintf("cannot load phone with userID [%s] and phoneID [%s]", userID, phoneID)
		return service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	if err = service.repository.Delete(ctx, userID, phoneID); err != nil {
		msg := fmt.Sprintf("cannot delete phone with id [%s] and user id [%s]", phoneID, userID)
		return service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	ctxLogger.Info(fmt.Sprintf("deleted phone with id [%s] and user id [%s]", phoneID, userID))

	event, err := service.createPhoneDeletedEvent(source, events.PhoneDeletedPayload{
		PhoneID:   phone.ID,
		UserID:    phone.UserID,
		Timestamp: phone.UpdatedAt,
		Owner:     phone.PhoneNumber,
	})
	if err != nil {
		msg := fmt.Sprintf("cannot create event when phone is deleted")
		return service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	if err = service.dispatcher.Dispatch(ctx, event); err != nil {
		msg := fmt.Sprintf("cannot dispatch event [%s] for phone with id [%s]", event.Type(), phone.ID)
		return service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	return nil
}

func (service *PhoneService) createPhone(ctx context.Context, params PhoneUpsertParams) (*entities.Phone, error) {
	ctx, span := service.tracer.Start(ctx)
	defer span.End()

	phone := &entities.Phone{
		ID:                       uuid.New(),
		UserID:                   params.UserID,
		FcmToken:                 params.FcmToken,
		MessagesPerMinute:        0,
		MessageExpirationSeconds: 0,
		PhoneNumber:              phonenumbers.Format(&params.PhoneNumber, phonenumbers.E164),
		CreatedAt:                time.Now().UTC(),
		UpdatedAt:                time.Now().UTC(),
	}

	if err := service.repository.Save(ctx, phone); err != nil {
		msg := fmt.Sprintf("cannot create phone with id [%s] and number [%s]", phone.ID, phone.PhoneNumber)
		return nil, service.tracer.WrapErrorSpan(span, stacktrace.Propagate(err, msg))
	}

	return phone, nil
}

func (service *PhoneService) createPhoneUpdatedEvent(source string, payload events.PhoneUpdatedPayload) (cloudevents.Event, error) {
	return service.createEvent(events.EventTypePhoneUpdated, source, payload)
}

func (service *PhoneService) createPhoneDeletedEvent(source string, payload events.PhoneDeletedPayload) (cloudevents.Event, error) {
	return service.createEvent(events.EventTypePhoneDeleted, source, payload)
}

func (service *PhoneService) update(phone *entities.Phone, params PhoneUpsertParams) *entities.Phone {
	if phone.FcmToken != nil {
		phone.FcmToken = params.FcmToken
	}
	if params.MessagesPerMinute != nil {
		phone.MessagesPerMinute = *params.MessagesPerMinute
	}

	if params.MessageExpirationDuration != nil {
		phone.MessageExpirationSeconds = uint(params.MessageExpirationDuration.Seconds())
	}
	return phone
}
