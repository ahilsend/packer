package common

import (
	"fmt"
	"log"
	"regexp"

	"github.com/hashicorp/packer/template/interpolate"
)

// AMIConfig is for common configuration related to creating AMIs.
type AMIConfig struct {
	AMIName                 string            `mapstructure:"ami_name"`
	AMIDescription          string            `mapstructure:"ami_description"`
	AMIVirtType             string            `mapstructure:"ami_virtualization_type"`
	AMIUsers                []string          `mapstructure:"ami_users"`
	AMIGroups               []string          `mapstructure:"ami_groups"`
	AMIProductCodes         []string          `mapstructure:"ami_product_codes"`
	AMIRegions              []string          `mapstructure:"ami_regions"`
	AMISkipRegionValidation bool              `mapstructure:"skip_region_validation"`
	AMITags                 TagMap            `mapstructure:"tags"`
	AMIENASupport           *bool             `mapstructure:"ena_support"`
	AMISriovNetSupport      bool              `mapstructure:"sriov_support"`
	AMIForceDeregister      bool              `mapstructure:"force_deregister"`
	AMIForceDeleteSnapshot  bool              `mapstructure:"force_delete_snapshot"`
	AMIEncryptBootVolume    *bool             `mapstructure:"encrypt_boot"`
	AMIKmsKeyId             string            `mapstructure:"kms_key_id"`
	AMIRegionKMSKeyIDs      map[string]string `mapstructure:"region_kms_key_ids"`
	SnapshotTags            TagMap            `mapstructure:"snapshot_tags"`
	SnapshotUsers           []string          `mapstructure:"snapshot_users"`
	SnapshotGroups          []string          `mapstructure:"snapshot_groups"`
	AMISkipBuildRegion      bool              `mapstructure:"skip_save_build_region"`
}

func stringInSlice(s []string, searchstr string) bool {
	for _, item := range s {
		if item == searchstr {
			return true
		}
	}
	return false
}

func (c *AMIConfig) Prepare(accessConfig *AccessConfig, ctx *interpolate.Context) []error {
	var errs []error

	if c.AMIName == "" {
		errs = append(errs, fmt.Errorf("ami_name must be specified"))
	}

	// Make sure that if we have region_kms_key_ids defined,
	//  the regions in region_kms_key_ids are also in ami_regions
	if len(c.AMIRegionKMSKeyIDs) > 0 {
		for kmsKeyRegion := range c.AMIRegionKMSKeyIDs {
			if !stringInSlice(c.AMIRegions, kmsKeyRegion) {
				errs = append(errs, fmt.Errorf("Region %s is in region_kms_key_ids but not in ami_regions", kmsKeyRegion))
			}
		}
	}

	errs = append(errs, c.prepareRegions(accessConfig)...)

	// Prevent sharing of default KMS key encrypted volumes with other aws users
	if len(c.AMIUsers) > 0 {
		if len(c.AMIKmsKeyId) == 0 && c.AMIEncryptBootVolume != nil && *c.AMIEncryptBootVolume {
			errs = append(errs, fmt.Errorf("Cannot share AMI encrypted with default KMS key"))
		}
		if len(c.AMIRegionKMSKeyIDs) > 0 {
			for _, kmsKey := range c.AMIRegionKMSKeyIDs {
				if len(kmsKey) == 0 {
					errs = append(errs, fmt.Errorf("Cannot share AMI encrypted with default KMS key for other regions"))
				}
			}
		}
	}

	var kmsKeys []string
	if len(c.AMIKmsKeyId) > 0 {
		kmsKeys = append(kmsKeys, c.AMIKmsKeyId)
	}
	if len(c.AMIRegionKMSKeyIDs) > 0 {
		for _, kmsKey := range c.AMIRegionKMSKeyIDs {
			if len(kmsKey) == 0 {
				kmsKeys = append(kmsKeys, c.AMIKmsKeyId)
			}
		}
	}
	for _, kmsKey := range kmsKeys {
		if !validateKmsKey(kmsKey) {
			errs = append(errs, fmt.Errorf("%s is not a valid KMS Key Id.", kmsKey))
		}
	}

	if len(c.SnapshotUsers) > 0 {
		if len(c.AMIKmsKeyId) == 0 && c.AMIEncryptBootVolume != nil && *c.AMIEncryptBootVolume {
			errs = append(errs, fmt.Errorf("Cannot share snapshot encrypted with default KMS key"))
		}
		if len(c.AMIRegionKMSKeyIDs) > 0 {
			for _, kmsKey := range c.AMIRegionKMSKeyIDs {
				if len(kmsKey) == 0 {
					errs = append(errs, fmt.Errorf("Cannot share snapshot encrypted with default KMS key"))
				}
			}
		}
	}

	if len(c.AMIName) < 3 || len(c.AMIName) > 128 {
		errs = append(errs, fmt.Errorf("ami_name must be between 3 and 128 characters long"))
	}

	if c.AMIName != templateCleanAMIName(c.AMIName) {
		errs = append(errs, fmt.Errorf("AMIName should only contain "+
			"alphanumeric characters, parentheses (()), square brackets ([]), spaces "+
			"( ), periods (.), slashes (/), dashes (-), single quotes ('), at-signs "+
			"(@), or underscores(_). You can use the `clean_resource_name` template "+
			"filter to automatically clean your ami name."))
	}

	if len(errs) > 0 {
		return errs
	}

	return nil
}

func (c *AMIConfig) prepareRegions(accessConfig *AccessConfig) (errs []error) {
	if len(c.AMIRegions) > 0 {
		regionSet := make(map[string]struct{})
		regions := make([]string, 0, len(c.AMIRegions))

		for _, region := range c.AMIRegions {
			// If we already saw the region, then don't look again
			if _, ok := regionSet[region]; ok {
				continue
			}

			// Mark that we saw the region
			regionSet[region] = struct{}{}

			// Make sure that if we have region_kms_key_ids defined,
			// the regions in ami_regions are also in region_kms_key_ids
			if len(c.AMIRegionKMSKeyIDs) > 0 {
				if _, ok := c.AMIRegionKMSKeyIDs[region]; !ok {
					errs = append(errs, fmt.Errorf("Region %s is in ami_regions but not in region_kms_key_ids", region))
				}
			}
			if (accessConfig != nil) && (region == accessConfig.RawRegion) {
				// make sure we don't try to copy to the region we originally
				// create the AMI in.
				log.Printf("Cannot copy AMI to AWS session region '%s', deleting it from `ami_regions`.", region)
				continue
			}
			regions = append(regions, region)
		}

		c.AMIRegions = regions
	}
	return errs
}

// See https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CopyImage.html
func validateKmsKey(kmsKey string) (valid bool) {
	kmsKeyIdPattern := `[a-f0-9-]+$`
	aliasPattern := `alias/[a-zA-Z0-9:/_-]+$`
	kmsArnStartPattern := `^arn:aws(-us-gov)?:kms:([a-z]{2}-(gov-)?[a-z]+-\d{1})?:(\d{12}):`
	if regexp.MustCompile(fmt.Sprintf("^%s", kmsKeyIdPattern)).MatchString(kmsKey) {
		return true
	}
	if regexp.MustCompile(fmt.Sprintf("^%s", aliasPattern)).MatchString(kmsKey) {
		return true
	}
	if regexp.MustCompile(fmt.Sprintf("%skey/%s", kmsArnStartPattern, kmsKeyIdPattern)).MatchString(kmsKey) {
		return true
	}
	if regexp.MustCompile(fmt.Sprintf("%s%s", kmsArnStartPattern, aliasPattern)).MatchString(kmsKey) {
		return true
	}
	return false
}
