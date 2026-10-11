package domain

import (
	"errors"
	"testing"
)

func TestCheckAdminLoss(t *testing.T) {
	admin := Membership{Role: RoleAdmin, Status: MembershipActive}
	tests := []struct {
		name         string
		m            Membership
		activeAdmins int
		want         error
	}{
		{"last admin", admin, 1, ErrLastAdmin},
		{"another admin left", admin, 2, nil},
		{"no admins counted", admin, 0, ErrLastAdmin},
		{"player", Membership{Role: RolePlayer, Status: MembershipActive}, 1, nil},
		{"pending admin", Membership{Role: RoleAdmin, Status: MembershipPending}, 1, nil},
		{"left admin", Membership{Role: RoleAdmin, Status: MembershipLeft}, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckAdminLoss(tt.m, tt.activeAdmins); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
