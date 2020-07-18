// Code generated by go-swagger; DO NOT EDIT.

package datacenter

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"fmt"
	"io"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"

	"github.com/kubermatic/kubermatic/pkg/test/e2e/api/utils/apiclient/models"
)

// ListDatacentersReader is a Reader for the ListDatacenters structure.
type ListDatacentersReader struct {
	formats strfmt.Registry
}

// ReadResponse reads a server response into the received o.
func (o *ListDatacentersReader) ReadResponse(response runtime.ClientResponse, consumer runtime.Consumer) (interface{}, error) {
	switch response.Code() {
	case 200:
		result := NewListDatacentersOK()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return result, nil
	default:
		result := NewListDatacentersDefault(response.Code())
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		if response.Code()/100 == 2 {
			return result, nil
		}
		return nil, result
	}
}

// NewListDatacentersOK creates a ListDatacentersOK with default headers values
func NewListDatacentersOK() *ListDatacentersOK {
	return &ListDatacentersOK{}
}

/*ListDatacentersOK handles this case with default header values.

DatacenterList
*/
type ListDatacentersOK struct {
	Payload models.DatacenterList
}

func (o *ListDatacentersOK) Error() string {
	return fmt.Sprintf("[GET /api/v1/dc][%d] listDatacentersOK  %+v", 200, o.Payload)
}

func (o *ListDatacentersOK) GetPayload() models.DatacenterList {
	return o.Payload
}

func (o *ListDatacentersOK) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	// response payload
	if err := consumer.Consume(response.Body(), &o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewListDatacentersDefault creates a ListDatacentersDefault with default headers values
func NewListDatacentersDefault(code int) *ListDatacentersDefault {
	return &ListDatacentersDefault{
		_statusCode: code,
	}
}

/*ListDatacentersDefault handles this case with default header values.

errorResponse
*/
type ListDatacentersDefault struct {
	_statusCode int

	Payload *models.ErrorResponse
}

// Code gets the status code for the list datacenters default response
func (o *ListDatacentersDefault) Code() int {
	return o._statusCode
}

func (o *ListDatacentersDefault) Error() string {
	return fmt.Sprintf("[GET /api/v1/dc][%d] listDatacenters default  %+v", o._statusCode, o.Payload)
}

func (o *ListDatacentersDefault) GetPayload() *models.ErrorResponse {
	return o.Payload
}

func (o *ListDatacentersDefault) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.ErrorResponse)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}